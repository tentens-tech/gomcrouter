package upstream

import (
	"github.com/pkg/errors"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/observability/metric"
	"github.com/tentens-tech/gomcrouter/internal/upstream/io"
	"sync"
)

type OrderedPool struct {
	ctx *config.AppContext
	mu  sync.Mutex

	healthyHosts   []*OrderedHost
	unhealthyHosts []*OrderedHost
}

type OrderedHost struct {
	*Host
	num      int
	hostname string
}

func NewOrderedPool(ctx *config.AppContext, cfg config.OrderedPoolConfig, eng *io.Engine) *OrderedPool {
	op := OrderedPool{
		ctx: ctx,
	}
	for num, hostname := range cfg {
		h := NewHost(ctx, hostname, op.onHostHealthy, op.onHostUnhealthy, num, eng)
		err := h.Heat()
		if err == nil {
			op.healthyHosts = append(op.healthyHosts, &OrderedHost{h, num, hostname})
			ctx.Logger.Infof("added healthy host: %s order: %d", hostname, num)
		} else {
			ctx.Logger.Warn(errors.Wrapf(err, "failed to heat host: %s", hostname))
			op.unhealthyHosts = append(op.unhealthyHosts, &OrderedHost{h, num, hostname})
			ctx.Logger.Infof("added unhealthy host: %s order: %d", hostname, num)
		}
	}

	metric.Collector.RegisterScraper(op.scrapeNumHealthyHostsMetric)
	return &op
}

func (o *OrderedPool) onHostUnhealthy(hid int) {
	o.mu.Lock()
	defer o.mu.Unlock()

	idx := -1
	for i, hh := range o.healthyHosts {
		if hh.num == hid {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}

	h := o.healthyHosts[idx]
	o.healthyHosts = append(o.healthyHosts[:idx], o.healthyHosts[idx+1:]...)
	o.unhealthyHosts = append(o.unhealthyHosts, h)

	o.ctx.Logger.Warnf("host %s went unhealthy", h.hostname)
}

func (o *OrderedPool) onHostHealthy(hid int) {
	o.mu.Lock()
	defer o.mu.Unlock()

	var h *OrderedHost
	for i, uh := range o.unhealthyHosts {
		if uh.num == hid {
			h = uh
			o.unhealthyHosts = append(o.unhealthyHosts[:i], o.unhealthyHosts[i+1:]...)
			break
		}
	}
	if h == nil {
		return
	}

	i := 0
	for ; i < len(o.healthyHosts); i++ {
		if h.num < o.healthyHosts[i].num {
			break
		}
	}
	o.healthyHosts = append(o.healthyHosts, nil)
	copy(o.healthyHosts[i+1:], o.healthyHosts[i:])
	o.healthyHosts[i] = h

	o.ctx.Logger.Infof("host %s went healthy", h.hostname)
}

func (o *OrderedPool) All() []*OrderedHost {
	return o.healthyHosts
}

func (o *OrderedPool) scrapeNumHealthyHostsMetric() {
	metric.Collector.HandleHealthyHostsCount(len(o.healthyHosts))
}
