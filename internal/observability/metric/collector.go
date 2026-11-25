package metric

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tentens-tech/gomcrouter/internal/machinery/registry"
	"github.com/tentens-tech/gomcrouter/internal/machinery/ring"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/version"
)

const (
	RingSize        = 65536
	MaxProcessBatch = 4096

	// DefaultScrapeInterval is how often registered scrapers are invoked.
	DefaultScrapeInterval = 5 * time.Second
	// idleProcessInterval is how long the async consumer sleeps when both
	// event rings were empty.
	idleProcessInterval = 50 * time.Millisecond
)

const Namespace = "gomcrouter"

const (
	RequestsTotalMetric         = "requests_total"
	UpstreamRequestsTotalMetric = "upstream_requests_total"

	UpstreamErrorsTotalMetric       = "upstream_errors_total"
	UpstreamHealthyHostsTotalMetric = "upstream_healthy_hosts_total"
	UpstreamConnectionsTotalMetric  = "upstream_connections_total"

	UpstreamGetHitsTotalMetric   = "upstream_get_hits_total"
	UpstreamGetMissesTotalMetric = "upstream_get_misses_total"

	RequestDurationMetric         = "request_duration_milliseconds"
	UpstreamRequestDurationMetric = "upstream_request_duration_milliseconds"

	MetricsEventsDroppedTotalMetric = "metrics_events_dropped_total"

	VersionMetric = "version"
)

// Event-type label values for MetricsEventsDroppedTotalMetric.
const (
	EventTypeRequest         = "request"
	EventTypeUpstreamRequest = "upstream_request"
)

var MilliBuckets = []float64{.1, .2, .3, .5, 1, 2, 3, 4, 5, 6, 7, 10, 25, 30, 50, 100, 150, 200, 250, 300, 400, 500, 700, 1000, 2000, 3000, 5000}

var Collector = newCollector(prometheus.DefaultRegisterer)

type collector struct {
	requestsTotalCounter         *prometheus.CounterVec
	upstreamRequestsTotalCounter *prometheus.CounterVec

	upstreamErrorsTotalCounter *prometheus.CounterVec
	upstreamHealthyHostsTotal  prometheus.Gauge
	upstreamConnectionsTotal   *prometheus.GaugeVec

	upstreamGetHitsCounter   *prometheus.CounterVec
	upstreamGetMissesCounter *prometheus.CounterVec

	fullRequestDurationHist     *prometheus.HistogramVec
	upstreamRequestDurationHist *prometheus.HistogramVec

	metricsEventsDroppedCounter *prometheus.CounterVec
	// Pre-resolved children to avoid label-map lookups (and the variadic
	// allocation that `WithLabelValues` performs) on the producer hot path.
	requestEventsDropped         prometheus.Counter
	upstreamRequestEventsDropped prometheus.Counter

	version *prometheus.GaugeVec

	scrapeInterval time.Duration
	serveOnce      sync.Once
	// stop terminates the goroutines started by Serve when closed. It is
	// never closed in production (the collector lives for the whole process).
	stop chan struct{}

	scrapersMu sync.Mutex
	scrapers   []func()

	// The rings are multi-producer: request events are pushed from every gnet
	// event loop (server-event-loops can be > 1) and upstream events from the
	// upstream engine loop, while processAsyncMetrics is the single consumer.
	requestEventsRing         *ring.MPSC[types.RequestEvent]
	upstreamRequestEventsRing *ring.MPSC[types.UpstreamRequestEvent]
}

func newCollector(reg prometheus.Registerer) *collector {
	c := &collector{
		scrapeInterval: DefaultScrapeInterval,
		stop:           make(chan struct{}),
		requestsTotalCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      RequestsTotalMetric,
				Help:      "Total number of requests made by clients",
			},
			[]string{"cmd"},
		),
		upstreamRequestsTotalCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      UpstreamRequestsTotalMetric,
				Help:      "Total number of requests made to upstream instances",
			},
			[]string{"cmd", "host"},
		),
		upstreamErrorsTotalCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      UpstreamErrorsTotalMetric,
				Help:      "Total number of errors returned from upstream instances",
			},
			[]string{"cmd", "host"},
		),
		upstreamHealthyHostsTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      UpstreamHealthyHostsTotalMetric,
			Help:      "Total number of upstream healthy hosts",
		}),
		upstreamConnectionsTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Name:      UpstreamConnectionsTotalMetric,
				Help:      "Total number of upstream connections",
			},
			[]string{"host"},
		),
		upstreamGetHitsCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      UpstreamGetHitsTotalMetric,
				Help:      "Total number of upstream get/gets responses that returned at least one VALUE",
			},
			[]string{"host"},
		),
		upstreamGetMissesCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      UpstreamGetMissesTotalMetric,
				Help:      "Total number of upstream get/gets responses that returned no VALUE (bare END)",
			},
			[]string{"host"},
		),
		fullRequestDurationHist: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Name:      RequestDurationMetric,
				Help:      "Histogram of gomcrouter request latency (milliseconds).",
				Buckets:   MilliBuckets,
			},
			[]string{"cmd"},
		),
		upstreamRequestDurationHist: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Name:      UpstreamRequestDurationMetric,
				Help:      "Histogram of gomcrouter upstream request latency (milliseconds).",
				Buckets:   MilliBuckets,
			},
			[]string{"cmd", "host"},
		),
		metricsEventsDroppedCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      MetricsEventsDroppedTotalMetric,
				Help:      "Total number of metric events dropped because the async event ring was full",
			},
			[]string{"event_type"},
		),
		version: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Name:      VersionMetric,
				Help:      "Gomcrouter version",
			},
			[]string{"version"},
		),
	}

	c.requestEventsDropped = c.metricsEventsDroppedCounter.WithLabelValues(EventTypeRequest)
	c.upstreamRequestEventsDropped = c.metricsEventsDroppedCounter.WithLabelValues(EventTypeUpstreamRequest)

	c.requestEventsRing = ring.NewMPSC[types.RequestEvent](RingSize)
	c.upstreamRequestEventsRing = ring.NewMPSC[types.UpstreamRequestEvent](RingSize)

	reg.MustRegister(c.fullRequestDurationHist)
	reg.MustRegister(c.requestsTotalCounter)
	reg.MustRegister(c.upstreamRequestDurationHist)
	reg.MustRegister(c.upstreamErrorsTotalCounter)
	reg.MustRegister(c.upstreamHealthyHostsTotal)
	reg.MustRegister(c.upstreamConnectionsTotal)
	reg.MustRegister(c.upstreamRequestsTotalCounter)
	reg.MustRegister(c.upstreamGetHitsCounter)
	reg.MustRegister(c.upstreamGetMissesCounter)
	reg.MustRegister(c.metricsEventsDroppedCounter)
	reg.MustRegister(c.version)

	// push version metric once
	c.version.WithLabelValues(version.Version).Set(1)
	return c
}

// Serve starts the scraper and async-metrics consumer goroutines. It is
// idempotent: the event rings are single-consumer, so a second consumer
// must never be started.
func (c *collector) Serve() {
	c.serveOnce.Do(func() {
		go c.serveScrapers(c.stop)
		go c.processAsyncMetrics(c.stop)
	})
}

func (c *collector) HandleRequestAsync(cmd int, durationNs int64) {
	if !c.requestEventsRing.Push(types.RequestEvent{
		Cmd:        cmd,
		DurationNs: durationNs,
	}) {
		c.requestEventsDropped.Inc()
	}
}

func (c *collector) HandleUpstreamRequestAsync(cmd, hostId int, durationNs int64, hitStatus int8) {
	if !c.upstreamRequestEventsRing.Push(types.UpstreamRequestEvent{
		Cmd:        cmd,
		DurationNs: durationNs,
		HostId:     hostId,
		HitStatus:  hitStatus,
	}) {
		c.upstreamRequestEventsDropped.Inc()
	}
}

func (c *collector) HandleUpstreamError(cmd, hostId int) {
	c.upstreamErrorsTotalCounter.WithLabelValues(
		string(ascii.Commands[cmd]),
		registry.GlobalHostRegistry.GetHost(hostId),
	).Inc()
}

func (c *collector) HandleHealthyHostsCount(cnt int) {
	c.upstreamHealthyHostsTotal.Set(float64(cnt))
}

func (c *collector) HandleConnectionCount(hostId int, cnt int) {
	c.upstreamConnectionsTotal.WithLabelValues(
		registry.GlobalHostRegistry.GetHost(hostId),
	).Set(float64(cnt))
}

func (c *collector) RegisterScraper(scrapeFunc func()) {
	c.scrapersMu.Lock()
	c.scrapers = append(c.scrapers, scrapeFunc)
	c.scrapersMu.Unlock()
}

func (c *collector) runScrapers() {
	c.scrapersMu.Lock()
	scrapers := c.scrapers
	c.scrapersMu.Unlock()

	for _, scraper := range scrapers {
		scraper()
	}
}

// serveScrapers invokes the registered scrapers every scrapeInterval until
// stop is closed.
func (c *collector) serveScrapers(stop <-chan struct{}) {
	ticker := time.NewTicker(c.scrapeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c.runScrapers()
		}
	}
}

// processAsyncMetrics drains the event rings until stop is closed, sleeping
// briefly whenever both are empty.
func (c *collector) processAsyncMetrics(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}

		if !c.processBatch() {
			time.Sleep(idleProcessInterval)
		}
	}
}

// processBatch consumes up to MaxProcessBatch events from each ring and
// reports whether any event was consumed. Must only be called from the
// single consumer goroutine.
func (c *collector) processBatch() bool {
	didWork := false

	for i := 0; i < MaxProcessBatch; i++ {
		ev, ok := c.requestEventsRing.Pop()
		if !ok {
			break
		}

		didWork = true

		cmdString := string(ascii.Commands[ev.Cmd])

		c.requestsTotalCounter.WithLabelValues(cmdString).Inc()
		c.fullRequestDurationHist.WithLabelValues(cmdString).Observe(float64(ev.DurationNs) / 1e6)
	}

	for i := 0; i < MaxProcessBatch; i++ {
		ev, ok := c.upstreamRequestEventsRing.Pop()
		if !ok {
			break
		}

		didWork = true

		cmdString := string(ascii.Commands[ev.Cmd])
		host := registry.GlobalHostRegistry.GetHost(ev.HostId)

		c.upstreamRequestsTotalCounter.WithLabelValues(cmdString, host).Inc()
		c.upstreamRequestDurationHist.WithLabelValues(cmdString, host).Observe(float64(ev.DurationNs) / 1e6)

		switch ev.HitStatus {
		case ascii.GetResponseHit:
			c.upstreamGetHitsCounter.WithLabelValues(host).Inc()
		case ascii.GetResponseMiss:
			c.upstreamGetMissesCounter.WithLabelValues(host).Inc()
		}
	}

	return didWork
}
