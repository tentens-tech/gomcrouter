package upstream

import (
	"fmt"
	"github.com/pkg/errors"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/registry"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream/io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxResolveRetryCount = 10
)

var (
	ErrNoActiveConnection = errors.New("no active connection")
)

type Host struct {
	num int
	id  int
	ctx *config.AppContext

	hostname     string
	resolvedAddr string

	mu        sync.Mutex
	engine    *io.Engine
	pollGroup atomic.Pointer[io.PollGroup]

	onHealthy   func(int)
	onUnhealthy func(int)
}

func NewHost(ctx *config.AppContext, hostname string, onHealthy, onUnhealthy func(int), num int, eng *io.Engine) *Host {
	id := registry.GlobalHostRegistry.RegisterHost(hostname)

	h := Host{
		id:          id,
		num:         num,
		ctx:         ctx,
		mu:          sync.Mutex{},
		hostname:    hostname,
		engine:      eng,
		onHealthy:   onHealthy,
		onUnhealthy: onUnhealthy,
	}

	go h.hostWorker()

	return &h
}

func (h *Host) AsyncDo(req *types.Request, triggerWakeup bool) {
	pg := h.pollGroup.Load()
	if pg == nil {
		req.Callback(nil, ErrNoActiveConnection)
		return
	}

	if !pg.Enqueue(req) {
		return
	}

	if triggerWakeup {
		h.engine.Wakeup()
	}
}

func (h *Host) Heat() error {
	resolved, err := h.resolveAddress()
	if err != nil {
		return err
	}

	h.mu.Lock()
	h.resolvedAddr = resolved
	h.mu.Unlock()

	return h.init()
}

func (h *Host) init() error {
	h.mu.Lock()
	addr := h.resolvedAddr
	h.mu.Unlock()

	pg, err := h.engine.EnrollHost(addr, h.id, h.drain)
	if err != nil {
		return err
	}

	if !h.pollGroup.CompareAndSwap(nil, pg) {
		// already initialized (lost race)
		pg.Drain()
		h.engine.DisenrollHost(pg)
		return nil
	}

	h.onHealthy(h.num)

	return nil
}

func (h *Host) hostWorker() {
	dnsTicker := time.NewTicker(h.ctx.Config.DnsCacheTTL)
	healthTicker := time.NewTicker(h.ctx.Config.HealthCheckInterval)
	defer dnsTicker.Stop()
	defer healthTicker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-dnsTicker.C:
			changed, err := h.isAddressChanged()
			if err != nil {
				h.ctx.Logger.Warnf("failed to check host address change: %v", err)
				continue
			}
			if changed {
				h.ctx.Logger.Warnf("host %s address changed to %s, reconnecting", h.hostname, h.resolvedAddr)
				h.handleAddressChanged()
			}
		case <-healthTicker.C:
			h.healthCheck()
		}
	}
}

func (h *Host) healthCheck() {
	pg := h.pollGroup.Load()

	err := h.connectionProbe()
	if err == nil {
		if pg != nil {
			// still healthy
			return
		}

		err = h.init()
		if err != nil {
			h.ctx.Logger.Warnf("failed to re-initialize host: %v", err)
			return
		}

		// became healthy
		return
	}

	if pg != nil {
		// became unhealthy
		h.ctx.Logger.Warnf("host %s failed connection probe: %v", h.hostname, err)
		h.drain()
		return
	}
}

func (h *Host) handleAddressChanged() {
	h.drain()

	err := h.connectionProbe()
	if err != nil {
		h.ctx.Logger.Warnf("host %s failed connection probe to new address: %v", h.hostname, err)
		return
	}

	err = h.init()
	if err != nil {
		h.ctx.Logger.Warnf("host %s failed init on a new new address: %v", h.hostname, err)
	}

	// became healthy
}

func (h *Host) connectionProbe() error {
	h.mu.Lock()
	addr := h.resolvedAddr
	h.mu.Unlock()
	if addr == "" {
		addr = h.hostname
	}

	cn, err := net.DialTimeout("tcp", addr, time.Duration(h.ctx.Config.TimeoutMillis)*time.Millisecond)
	if err != nil {
		return err
	}

	_ = cn.Close()

	return nil
}

func (h *Host) isAddressChanged() (bool, error) {
	resolved, err := h.resolveAddress()
	if err != nil {
		return false, err
	}

	h.mu.Lock()
	cur := h.resolvedAddr
	if resolved != cur {
		h.resolvedAddr = resolved
		h.mu.Unlock()
		return true, nil
	}

	h.mu.Unlock()

	return false, nil
}

func (h *Host) resolveAddress() (string, error) {
	host, port, err := net.SplitHostPort(h.hostname)
	if err != nil {
		return "", err
	}

	for i := 0; i < MaxResolveRetryCount; i++ {
		ips, err := net.LookupIP(host)
		if err != nil {
			h.ctx.Logger.Debug("failed to resolve address err:", err)
			continue
		}

		for _, ip := range ips {
			if ip.To4() != nil {
				return net.JoinHostPort(ip.String(), port), nil
			}
		}
	}

	return "", fmt.Errorf("no addresses resolved")
}

// is called by worker goroutine or eventLoop. should drain pollGroup and DisenrollHost then.
func (h *Host) drain() {
	pg := h.pollGroup.Swap(nil)
	if pg == nil {
		// already drained (lost race)
		return
	}

	h.onUnhealthy(h.num)
	pg.Drain()
	h.engine.DisenrollHost(pg)
}
