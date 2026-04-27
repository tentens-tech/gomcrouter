package upstream

import (
	"sync"
	"sync/atomic"

	"github.com/pkg/errors"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/observability/metric"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream/io"
)

// AsyncDoer is the contract upstream hosts expose to request handlers.
// Defining it as an interface allows handlers to be tested in isolation
// from the real gnet/netpoll-backed *Host.
type AsyncDoer interface {
	AsyncDo(req *types.Request, triggerWakeup bool)
}

type OrderedPool struct {
	ctx *config.AppContext
	mu  sync.Mutex

	healthyHosts []*OrderedHost
	// healthyHostsView is published lock-free and read by All() on the
	// request hot path. Writes happen only under o.mu, from refreshView.
	// Using atomic.Pointer makes the slice-header publication race-free
	// (a slice header is three words; a plain assignment is not atomic).
	healthyHostsView atomic.Pointer[[]AsyncDoer]
	unhealthyHosts   []*OrderedHost
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

	op.refreshView()

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

	o.refreshView()

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

	o.refreshView()

	o.ctx.Logger.Infof("host %s went healthy", h.hostname)
}

func (o *OrderedPool) All() []AsyncDoer {
	p := o.healthyHostsView.Load()
	if p == nil {
		return nil
	}
	return *p
}

// refreshView rebuilds the AsyncDoer snapshot from healthyHosts.
// Caller must hold o.mu (or be a single-threaded constructor).
func (o *OrderedPool) refreshView() {
	if len(o.healthyHosts) == 0 {
		o.healthyHostsView.Store(nil)
		return
	}
	view := make([]AsyncDoer, len(o.healthyHosts))
	for i, h := range o.healthyHosts {
		view[i] = h
	}
	o.healthyHostsView.Store(&view)
}

func (o *OrderedPool) scrapeNumHealthyHostsMetric() {
	metric.Collector.HandleHealthyHostsCount(len(o.healthyHosts))
}
