package io

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/observability/metric"
	"github.com/tentens-tech/gomcrouter/internal/upstream/netpoll"
	"runtime"
	"sync"
	"sync/atomic"
)

const (
	DefaultRingSize = 8192
)

type Engine struct {
	open  atomic.Bool
	mu    sync.Mutex
	maint atomic.Bool

	ctx *config.AppContext

	pollGroups []*PollGroup
	poller     *netpoll.Poller
}

func NewEngine(ctx *config.AppContext) (*Engine, error) {
	poller, err := netpoll.NewPoller()
	if err != nil {
		return nil, err
	}

	e := &Engine{
		mu:     sync.Mutex{},
		ctx:    ctx,
		poller: poller,
	}

	e.open.Store(true)
	e.maint.Store(false)

	metric.Collector.RegisterScraper(e.scrapeHostConnectionCountMetric)

	return e, nil
}

func (e *Engine) Start() {
	e.eventLoop()
}

func (e *Engine) Stop() {
	e.open.CompareAndSwap(true, false)
	_ = e.poller.Wakeup()
}

func (e *Engine) EnrollHost(addr string, hostId int, onFailureHook func()) (*PollGroup, error) {
	pg, err := newPollGroup(
		addr,
		hostId,
		e.ctx,
		e.poller,
		e.onPollerFailure,
		onFailureHook,
	)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.pollGroups = append(e.pollGroups, pg)
	e.mu.Unlock()

	return pg, nil
}

func (e *Engine) DisenrollHost(pg *PollGroup) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// match by identity: several pollgroups may share an address
	for i, epg := range e.pollGroups {
		if epg == pg {
			e.pollGroups = append(e.pollGroups[:i], e.pollGroups[i+1:]...)
			return
		}
	}
}

func (e *Engine) Wakeup() {
	err := e.poller.Wakeup()
	if err != nil {
		e.onPollerFailure(err)
	}
}

func (e *Engine) eventLoop() {
	runtime.LockOSThread()
	e.ctx.Logger.Info("starting upstream engine event loop")
	for e.open.Load() {
		sleepMsec := -1
		for i := 0; i < len(e.pollGroups); i++ {
			if !e.pollGroups[i].IsActive() {
				continue
			}

			e.pollGroups[i].dispatch()
			sleepMsec = e.pollGroups[i].capSleep(sleepMsec)
		}

		err := e.poller.Poll(sleepMsec)
		if err != nil {
			e.onPollerFailure(err)
		}
	}
}

func (e *Engine) onPollerFailure(err error) {
	e.ctx.Logger.Errorf("poller failure: %v", err)
	e.ctx.Logger.Warn("trying to re-create poller...")
	e.recreatePoller()
}

func (e *Engine) recreatePoller() {
	if !e.maint.CompareAndSwap(false, true) {
		return
	}

	// iterate over a copy: failure hooks disenroll their pollgroup, which
	// shifts e.pollGroups and would skip groups while ranging over it.
	e.mu.Lock()
	pollGroups := append([]*PollGroup(nil), e.pollGroups...)
	e.mu.Unlock()

	for _, pg := range pollGroups {
		pg.onFailureHook()
	}

	e.poller.Close()

	newPoller, err := netpoll.NewPoller()
	if err != nil {
		e.open.Store(false)
		e.ctx.Logger.Errorf("fatal: failed to recreate poller %v", err)
		return
	}

	e.poller = newPoller
	e.ctx.Logger.Info("successfully recreated poller.")

	e.maint.Store(false)
}

func (e *Engine) scrapeHostConnectionCountMetric() {
	for _, pg := range e.pollGroups {
		metric.Collector.HandleConnectionCount(pg.HostId(), pg.NumSockets())
	}
}
