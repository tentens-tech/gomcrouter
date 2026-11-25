package metric

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/machinery/registry"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/version"
)

// newTestCollector returns a collector registered on its own registry, so
// tests do not collide with the global Collector (or with each other).
func newTestCollector(t *testing.T) (*collector, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	return newCollector(reg), reg
}

// registerHost registers a uniquely named host in the global registry and
// returns its id and name.
func registerHost(t *testing.T) (int, string) {
	t.Helper()
	name := t.Name() + ":11211"
	return registry.GlobalHostRegistry.RegisterHost(name), name
}

// scrape renders every metric of reg in the text exposition format, exactly
// as Prometheus would scrape it.
func scrape(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

func TestNewCollector_ExportsVersion(t *testing.T) {
	_, reg := newTestCollector(t)

	expected := `
# HELP gomcrouter_version Gomcrouter version
# TYPE gomcrouter_version gauge
gomcrouter_version{version="` + version.Version + `"} 1
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected), Namespace+"_"+VersionMetric))
}

func TestNewCollector_DuplicateRegistrationPanics(t *testing.T) {
	reg := prometheus.NewRegistry()
	newCollector(reg)

	assert.Panics(t, func() { newCollector(reg) })
}

func TestGlobalCollector_RegisteredOnDefaultRegistry(t *testing.T) {
	require.NotNil(t, Collector)

	n, err := testutil.GatherAndCount(prometheus.DefaultGatherer, Namespace+"_"+VersionMetric)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestProcessBatch_EmptyRingsDoNoWork(t *testing.T) {
	c, _ := newTestCollector(t)

	assert.False(t, c.processBatch())
}

func TestHandleRequestAsync_CountsAndObservesDuration(t *testing.T) {
	c, reg := newTestCollector(t)

	c.HandleRequestAsync(ascii.GetCmd, int64(2*time.Millisecond))
	c.HandleRequestAsync(ascii.GetCmd, int64(500*time.Microsecond))
	c.HandleRequestAsync(ascii.SetCmd, int64(time.Millisecond))

	// nothing is recorded until the consumer drains the ring
	assert.Zero(t, testutil.CollectAndCount(c.requestsTotalCounter))

	require.True(t, c.processBatch())
	assert.False(t, c.processBatch(), "ring must be empty after a drain")

	assert.InDelta(t, 2, testutil.ToFloat64(c.requestsTotalCounter.WithLabelValues("get")), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(c.requestsTotalCounter.WithLabelValues("set")), 0)

	out := scrape(t, reg)
	assert.Contains(t, out, `gomcrouter_request_duration_milliseconds_count{cmd="get"} 2`)
	assert.Contains(t, out, `gomcrouter_request_duration_milliseconds_sum{cmd="get"} 2.5`)
	assert.Contains(t, out, `gomcrouter_request_duration_milliseconds_bucket{cmd="get",le="0.5"} 1`)
	assert.Contains(t, out, `gomcrouter_request_duration_milliseconds_bucket{cmd="get",le="2"} 2`)
	assert.Contains(t, out, `gomcrouter_request_duration_milliseconds_count{cmd="set"} 1`)
}

func TestHandleUpstreamRequestAsync_HitMissAndUnknown(t *testing.T) {
	c, reg := newTestCollector(t)
	hostID, host := registerHost(t)

	tests := []struct {
		name      string
		cmd       int
		hitStatus int8
	}{
		{name: "get hit", cmd: ascii.GetCmd, hitStatus: ascii.GetResponseHit},
		{name: "gets hit", cmd: ascii.GetsCmd, hitStatus: ascii.GetResponseHit},
		{name: "get miss", cmd: ascii.GetCmd, hitStatus: ascii.GetResponseMiss},
		{name: "get unknown", cmd: ascii.GetCmd, hitStatus: ascii.GetResponseUnknown},
		{name: "set unknown", cmd: ascii.SetCmd, hitStatus: ascii.GetResponseUnknown},
	}
	for _, tt := range tests {
		c.HandleUpstreamRequestAsync(tt.cmd, hostID, int64(3*time.Millisecond), tt.hitStatus)
	}

	require.True(t, c.processBatch())

	assert.InDelta(t, 3, testutil.ToFloat64(c.upstreamRequestsTotalCounter.WithLabelValues("get", host)), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(c.upstreamRequestsTotalCounter.WithLabelValues("gets", host)), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(c.upstreamRequestsTotalCounter.WithLabelValues("set", host)), 0)
	assert.InDelta(t, 2, testutil.ToFloat64(c.upstreamGetHitsCounter.WithLabelValues(host)), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(c.upstreamGetMissesCounter.WithLabelValues(host)), 0)

	out := scrape(t, reg)
	assert.Contains(t, out, `gomcrouter_upstream_request_duration_milliseconds_count{cmd="get",host="`+host+`"} 3`)
	assert.Contains(t, out, `gomcrouter_upstream_request_duration_milliseconds_sum{cmd="get",host="`+host+`"} 9`)
}

func TestProcessBatch_IsBoundedByMaxProcessBatch(t *testing.T) {
	c, _ := newTestCollector(t)
	hostID, host := registerHost(t)

	total := MaxProcessBatch + 10
	for i := 0; i < total; i++ {
		c.HandleRequestAsync(ascii.GetCmd, 1)
		c.HandleUpstreamRequestAsync(ascii.GetCmd, hostID, 1, ascii.GetResponseUnknown)
	}

	require.True(t, c.processBatch())
	assert.InDelta(t, MaxProcessBatch, testutil.ToFloat64(c.requestsTotalCounter.WithLabelValues("get")), 0)
	assert.InDelta(t, MaxProcessBatch, testutil.ToFloat64(c.upstreamRequestsTotalCounter.WithLabelValues("get", host)), 0)

	require.True(t, c.processBatch())
	assert.InDelta(t, total, testutil.ToFloat64(c.requestsTotalCounter.WithLabelValues("get")), 0)
	assert.InDelta(t, total, testutil.ToFloat64(c.upstreamRequestsTotalCounter.WithLabelValues("get", host)), 0)

	assert.False(t, c.processBatch())
}

func TestHandleAsync_FullRingDropsAndCounts(t *testing.T) {
	c, reg := newTestCollector(t)
	hostID, _ := registerHost(t)

	const extra = 3
	for i := 0; i < RingSize+extra; i++ {
		c.HandleRequestAsync(ascii.GetCmd, 1)
		c.HandleUpstreamRequestAsync(ascii.GetCmd, hostID, 1, ascii.GetResponseMiss)
	}

	assert.InDelta(t, extra, testutil.ToFloat64(c.requestEventsDropped), 0)
	assert.InDelta(t, extra, testutil.ToFloat64(c.upstreamRequestEventsDropped), 0)

	expected := `
# HELP gomcrouter_metrics_events_dropped_total Total number of metric events dropped because the async event ring was full
# TYPE gomcrouter_metrics_events_dropped_total counter
gomcrouter_metrics_events_dropped_total{event_type="request"} 3
gomcrouter_metrics_events_dropped_total{event_type="upstream_request"} 3
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected), Namespace+"_"+MetricsEventsDroppedTotalMetric))

	// draining frees space: the next event is accepted, not dropped
	for c.processBatch() {
	}
	assert.InDelta(t, RingSize, testutil.ToFloat64(c.requestsTotalCounter.WithLabelValues("get")), 0)

	c.HandleRequestAsync(ascii.GetCmd, 1)
	assert.InDelta(t, extra, testutil.ToFloat64(c.requestEventsDropped), 0)
	require.True(t, c.processBatch())
	assert.InDelta(t, RingSize+1, testutil.ToFloat64(c.requestsTotalCounter.WithLabelValues("get")), 0)
}

// TestHandleAsync_ConcurrentProducersAndConsumer models production: every
// gnet event loop (server-event-loops > 1) produces request events
// concurrently while the single consumer goroutine drains them. No event may
// be lost or double-counted, and the race detector must stay quiet.
func TestHandleAsync_ConcurrentProducersAndConsumer(t *testing.T) {
	c, _ := newTestCollector(t)
	hostID, host := registerHost(t)

	const (
		producers   = 4
		perProducer = 2000
	)

	stop := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		c.processAsyncMetrics(stop)
	}()

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				c.HandleRequestAsync(ascii.GetCmd, 1)
				c.HandleUpstreamRequestAsync(ascii.GetCmd, hostID, 1, ascii.GetResponseHit)
			}
		}()
	}
	wg.Wait()

	const want = producers * perProducer
	require.Eventually(t, func() bool {
		got := testutil.ToFloat64(c.requestsTotalCounter.WithLabelValues("get")) +
			testutil.ToFloat64(c.requestEventsDropped)
		gotUp := testutil.ToFloat64(c.upstreamRequestsTotalCounter.WithLabelValues("get", host)) +
			testutil.ToFloat64(c.upstreamRequestEventsDropped)
		return got == want && gotUp == want
	}, 5*time.Second, 5*time.Millisecond)

	close(stop)
	<-consumerDone

	assert.InDelta(t, want, testutil.ToFloat64(c.requestsTotalCounter.WithLabelValues("get"))+
		testutil.ToFloat64(c.requestEventsDropped), 0, "no event may be counted twice")
}

func TestProcessAsyncMetrics_StopsWhenStopClosed(t *testing.T) {
	c, _ := newTestCollector(t)

	stop := make(chan struct{})
	close(stop)

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.processAsyncMetrics(stop)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("processAsyncMetrics did not return after stop was closed")
	}
}

func TestHandleUpstreamError(t *testing.T) {
	c, _ := newTestCollector(t)
	hostID, host := registerHost(t)

	c.HandleUpstreamError(ascii.SetCmd, hostID)
	c.HandleUpstreamError(ascii.SetCmd, hostID)
	c.HandleUpstreamError(ascii.GetCmd, hostID)

	assert.InDelta(t, 2, testutil.ToFloat64(c.upstreamErrorsTotalCounter.WithLabelValues("set", host)), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(c.upstreamErrorsTotalCounter.WithLabelValues("get", host)), 0)
}

func TestHandleHealthyHostsCount(t *testing.T) {
	c, _ := newTestCollector(t)

	c.HandleHealthyHostsCount(3)
	assert.InDelta(t, 3, testutil.ToFloat64(c.upstreamHealthyHostsTotal), 0)

	c.HandleHealthyHostsCount(0)
	assert.InDelta(t, 0, testutil.ToFloat64(c.upstreamHealthyHostsTotal), 0)
}

func TestHandleConnectionCount(t *testing.T) {
	c, _ := newTestCollector(t)
	hostID, host := registerHost(t)

	c.HandleConnectionCount(hostID, 4)
	assert.InDelta(t, 4, testutil.ToFloat64(c.upstreamConnectionsTotal.WithLabelValues(host)), 0)

	c.HandleConnectionCount(hostID, 1)
	assert.InDelta(t, 1, testutil.ToFloat64(c.upstreamConnectionsTotal.WithLabelValues(host)), 0)
}

func TestRunScrapers_CallsEachRegisteredScraperInOrder(t *testing.T) {
	c, _ := newTestCollector(t)

	c.runScrapers() // no scrapers: no-op

	var calls []int
	c.RegisterScraper(func() { calls = append(calls, 1) })
	c.RegisterScraper(func() { calls = append(calls, 2) })

	c.runScrapers()
	c.runScrapers()

	assert.Equal(t, []int{1, 2, 1, 2}, calls)
}

func TestServeScrapers_TicksUntilStopped(t *testing.T) {
	c, _ := newTestCollector(t)
	c.scrapeInterval = time.Millisecond

	var calls atomic.Int64
	c.RegisterScraper(func() { calls.Add(1) })

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.serveScrapers(stop)
	}()

	require.Eventually(t, func() bool { return calls.Load() >= 3 }, 2*time.Second, time.Millisecond)

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveScrapers did not return after stop was closed")
	}
}

func TestServe_ConsumesEventsAndIsIdempotent(t *testing.T) {
	c, _ := newTestCollector(t)
	c.scrapeInterval = time.Millisecond

	var scrapes atomic.Int64
	c.RegisterScraper(func() { scrapes.Add(1) })

	// Calling Serve twice must not start a second consumer.
	c.Serve()
	c.Serve()
	t.Cleanup(func() { close(c.stop) })

	c.HandleRequestAsync(ascii.VersionCmd, 1)

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(c.requestsTotalCounter.WithLabelValues("version")) == 1
	}, 2*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return scrapes.Load() >= 1 }, 2*time.Second, time.Millisecond)
}

func TestRegisterScraper_ConcurrentWithServe(t *testing.T) {
	c, _ := newTestCollector(t)
	c.scrapeInterval = time.Millisecond

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.serveScrapers(stop)
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
	})

	var calls atomic.Int64
	for i := 0; i < 50; i++ {
		c.RegisterScraper(func() { calls.Add(1) })
	}

	require.Eventually(t, func() bool { return calls.Load() >= 50 }, 2*time.Second, time.Millisecond)
}

// The producers run on the request hot path and must not allocate, whether
// the event is accepted or dropped because the ring is full.
func TestHandleAsync_DoesNotAllocate(t *testing.T) {
	c, _ := newTestCollector(t)
	hostID, _ := registerHost(t)

	allocs := testing.AllocsPerRun(1000, func() {
		c.HandleRequestAsync(ascii.GetCmd, 1)
		c.HandleUpstreamRequestAsync(ascii.GetCmd, hostID, 1, ascii.GetResponseHit)
	})
	assert.Zero(t, allocs)

	for i := 0; i < RingSize; i++ {
		c.HandleRequestAsync(ascii.GetCmd, 1)
		c.HandleUpstreamRequestAsync(ascii.GetCmd, hostID, 1, ascii.GetResponseHit)
	}
	allocs = testing.AllocsPerRun(1000, func() {
		c.HandleRequestAsync(ascii.GetCmd, 1)
		c.HandleUpstreamRequestAsync(ascii.GetCmd, hostID, 1, ascii.GetResponseHit)
	})
	assert.Zero(t, allocs)
	assert.Positive(t, testutil.ToFloat64(c.requestEventsDropped))
}
