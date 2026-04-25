package metric

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tentens-tech/gomcrouter/internal/machinery/registry"
	"github.com/tentens-tech/gomcrouter/internal/machinery/ring"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/version"
	"time"
)

const (
	RingSize        = 65536
	MaxProcessBatch = 4096
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

var Collector = newCollector()

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

	scrapers                  []func()
	requestEventsRing         *ring.SPSC[types.RequestEvent]
	upstreamRequestEventsRing *ring.SPSC[types.UpstreamRequestEvent]
}

func newCollector() *collector {
	c := &collector{
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
				Help:      "Total number of errors retuned from upstream instances",
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

	c.requestEventsRing = ring.NewSPSC[types.RequestEvent](RingSize)
	c.upstreamRequestEventsRing = ring.NewSPSC[types.UpstreamRequestEvent](RingSize)

	prometheus.MustRegister(c.fullRequestDurationHist)
	prometheus.MustRegister(c.requestsTotalCounter)
	prometheus.MustRegister(c.upstreamRequestDurationHist)
	prometheus.MustRegister(c.upstreamErrorsTotalCounter)
	prometheus.MustRegister(c.upstreamHealthyHostsTotal)
	prometheus.MustRegister(c.upstreamConnectionsTotal)
	prometheus.MustRegister(c.upstreamRequestsTotalCounter)
	prometheus.MustRegister(c.upstreamGetHitsCounter)
	prometheus.MustRegister(c.upstreamGetMissesCounter)
	prometheus.MustRegister(c.metricsEventsDroppedCounter)
	prometheus.MustRegister(c.version)

	// push version metric once
	c.version.WithLabelValues(version.Version).Set(1)
	return c
}

func (c *collector) Serve() {
	go c.serveScrapers()
	go c.processAsyncMetrics()
}

func (c *collector) HandleRequestAsync(cmd int, durationNs int64) {
	if !c.requestEventsRing.CanPush() {
		c.requestEventsDropped.Inc()
		return
	}
	c.requestEventsRing.Push(types.RequestEvent{
		Cmd:        cmd,
		DurationNs: durationNs,
	})
}

func (c *collector) HandleUpstreamRequestAsync(cmd, hostId int, durationNs int64, hitStatus int8) {
	if !c.upstreamRequestEventsRing.CanPush() {
		c.upstreamRequestEventsDropped.Inc()
		return
	}
	c.upstreamRequestEventsRing.Push(types.UpstreamRequestEvent{
		Cmd:        cmd,
		DurationNs: durationNs,
		HostId:     hostId,
		HitStatus:  hitStatus,
	})
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
	c.scrapers = append(c.scrapers, scrapeFunc)
}

func (c *collector) serveScrapers() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		for _, scraper := range c.scrapers {
			scraper()
		}
	}
}

func (c *collector) processAsyncMetrics() {
	for {
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

		if !didWork {
			time.Sleep(time.Millisecond * 50)
		}
	}
}
