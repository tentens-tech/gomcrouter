package upstream

import (
	"context"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream/internal/upstreamtest"
	"github.com/tentens-tech/gomcrouter/internal/upstream/io"
)

const waitFor = 2 * time.Second

// env is an app context plus an engine, torn down in the right order: host
// workers first, then the engine (its event loop, if started, then its
// pollgroups).
type env struct {
	t      *testing.T
	ctx    *config.AppContext
	cancel context.CancelFunc
	eng    *io.Engine
}

func newEnv(t *testing.T, opts ...upstreamtest.Options) *env {
	t.Helper()

	ctx := upstreamtest.NewAppContext(t, opts...)
	cctx, cancel := context.WithCancel(ctx.Context)
	ctx.Context = cctx

	eng, err := io.NewEngine(ctx)
	require.NoError(t, err)

	e := &env{t: t, ctx: ctx, cancel: cancel, eng: eng}
	// registered first, so it runs last: after the host workers and the event
	// loop (whose cleanups wait for them) have exited
	t.Cleanup(eng.Close)
	t.Cleanup(cancel)
	return e
}

// startEngine runs the event loop. Only safe while no host worker touches
// the engine concurrently (the engine does not synchronise that).
func (e *env) startEngine() {
	e.t.Helper()
	done := make(chan struct{})
	go func() {
		e.eng.Start()
		close(done)
	}()
	e.t.Cleanup(func() {
		e.eng.Stop()
		<-done
	})
}

// healthLog records the health callbacks, which hosts call from their worker
// goroutine.
type healthLog struct {
	mu     sync.Mutex
	events []string
}

func (l *healthLog) add(ev string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *healthLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (e *env) newHost(hostname string, log *healthLog) *Host {
	e.t.Helper()

	h := NewHost(e.ctx, hostname,
		func(int) { log.add("healthy") },
		func(int) { log.add("unhealthy") },
		7, e.eng)
	stopHost(e.t, e.cancel, h)
	return h
}

// stopHost stops the host worker and drains the host at test end.
func stopHost(t *testing.T, cancel context.CancelFunc, h *Host) {
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.workerDone:
		case <-time.After(waitFor):
			// draining would race with the still running worker
			t.Error("host worker did not stop, not draining the host")
			return
		}
		h.drain()
	})
}

func callbackChan() (func(*types.ByteBuf, error), chan error) {
	ch := make(chan error, 2)
	return func(_ *types.ByteBuf, err error) { ch <- err }, ch
}

func awaitErr(t *testing.T, ch chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(waitFor):
		require.FailNow(t, "callback was not called in time")
		return nil
	}
}

func TestHost_HeatAndAsyncDo(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	e := newEnv(t)
	log := &healthLog{}
	h := e.newHost(srv.Addr(), log)

	require.NoError(t, h.Heat())
	assert.Equal(t, []string{"healthy"}, log.get())
	require.NotNil(t, h.pollGroup.Load())
	assert.Equal(t, srv.Addr(), h.resolvedAddr)

	e.startEngine()

	var resp []byte
	done := make(chan error, 1)
	req := &types.Request{
		Raw: &types.ByteBuf{B: []byte("version\r\n")},
		Cmd: ascii.VersionCmd,
		Callback: func(b *types.ByteBuf, err error) {
			if b != nil {
				resp = append([]byte(nil), b.B...)
			}
			done <- err
		},
	}
	h.AsyncDo(req, true)
	require.NoError(t, awaitErr(t, done))
	assert.Equal(t, "VERSION 1.6.0-fake\r\n", string(resp))
}

func TestHost_AsyncDoWithoutConnection(t *testing.T) {
	e := newEnv(t)
	h := e.newHost(upstreamtest.RefusedAddr(t), &healthLog{})

	cb, ch := callbackChan()
	h.AsyncDo(&types.Request{Cmd: ascii.GetCmd, Callback: cb}, true)
	require.ErrorIs(t, awaitErr(t, ch), ErrNoActiveConnection)
}

func TestHost_AsyncDoOnClosedPollGroup(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	e := newEnv(t)
	h := e.newHost(srv.Addr(), &healthLog{})
	require.NoError(t, h.Heat())

	// a request racing with a drain: the group is closed but still published
	h.pollGroup.Load().Drain()

	cb, ch := callbackChan()
	h.AsyncDo(&types.Request{Cmd: ascii.GetCmd, Callback: cb}, true)
	require.ErrorIs(t, awaitErr(t, ch), io.ErrClosed)
}

func TestHost_HeatFailures(t *testing.T) {
	tests := []struct {
		name     string
		hostname func(t *testing.T) string
	}{
		{name: "connection refused", hostname: func(t *testing.T) string { return upstreamtest.RefusedAddr(t) }},
		{name: "missing port", hostname: func(*testing.T) string { return "127.0.0.1" }},
		{name: "no ipv4 address", hostname: func(*testing.T) string { return "[::1]:11211" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			log := &healthLog{}
			h := e.newHost(tt.hostname(t), log)

			require.Error(t, h.Heat())
			assert.Nil(t, h.pollGroup.Load())
			assert.Empty(t, log.get())
		})
	}
}

func TestHost_ResolveAddressPrefersIPv4(t *testing.T) {
	e := newEnv(t)
	h := e.newHost("localhost:11211", &healthLog{})

	addr, err := h.resolveAddress()
	require.NoError(t, err)
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	assert.Equal(t, "11211", port)
	ip := net.ParseIP(host)
	require.NotNil(t, ip)
	assert.NotNil(t, ip.To4())
	assert.True(t, ip.IsLoopback())
}

func TestHost_HealthCheckFlipsHealth(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	e := newEnv(t)
	log := &healthLog{}
	h := e.newHost(srv.Addr(), log)
	require.NoError(t, h.Heat())

	// healthy and reachable: nothing happens
	h.healthCheck()
	assert.Equal(t, []string{"healthy"}, log.get())
	pg := h.pollGroup.Load()
	require.NotNil(t, pg)

	// unreachable: the host is drained
	srv.Stop()
	h.healthCheck()
	assert.Equal(t, []string{"healthy", "unhealthy"}, log.get())
	assert.Nil(t, h.pollGroup.Load())
	assert.False(t, pg.IsActive())

	// still unreachable: nothing happens
	h.healthCheck()
	assert.Equal(t, []string{"healthy", "unhealthy"}, log.get())

	// reachable again: reconnected
	srv.Restart()
	h.healthCheck()
	assert.Equal(t, []string{"healthy", "unhealthy", "healthy"}, log.get())
	require.NotNil(t, h.pollGroup.Load())
	assert.NotSame(t, pg, h.pollGroup.Load())
	assert.True(t, h.pollGroup.Load().IsActive())
}

func TestHost_HealthCheckReinitFailure(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	e := newEnv(t)
	log := &healthLog{}
	// never heated: the probe falls back to the hostname, but there is no
	// resolved address to connect the pollgroup to
	h := e.newHost(srv.Addr(), log)

	h.healthCheck()
	assert.Nil(t, h.pollGroup.Load())
	assert.Empty(t, log.get())
}

func TestHost_HealthWorker(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	e := newEnv(t, func(c *config.App) {
		c.HealthCheckInterval = 5 * time.Millisecond
		c.TimeoutMillis = 100
	})
	log := &healthLog{}
	h := e.newHost(srv.Addr(), log)
	require.NoError(t, h.Heat())

	srv.Stop()
	require.Eventually(t, func() bool { return h.pollGroup.Load() == nil }, waitFor, time.Millisecond)

	srv.Restart()
	// the replacement pollgroup is published before onHealthy is called, so
	// wait for the callback rather than for the group
	require.Eventually(t, func() bool { return len(log.get()) == 3 }, waitFor, time.Millisecond)
	assert.Equal(t, []string{"healthy", "unhealthy", "healthy"}, log.get())
	assert.NotNil(t, h.pollGroup.Load())
}

func TestHost_AddressChange(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	_, port, err := net.SplitHostPort(srv.Addr())
	require.NoError(t, err)

	e := newEnv(t)
	log := &healthLog{}
	h := e.newHost(net.JoinHostPort("localhost", port), log)
	require.NoError(t, h.Heat())
	assert.Equal(t, srv.Addr(), h.resolvedAddr)

	changed, err := h.isAddressChanged()
	require.NoError(t, err)
	assert.False(t, changed)

	// pretend the name used to resolve elsewhere
	h.mu.Lock()
	h.resolvedAddr = net.JoinHostPort("127.0.0.2", port)
	h.mu.Unlock()

	changed, err = h.isAddressChanged()
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, srv.Addr(), h.resolvedAddr)

	old := h.pollGroup.Load()
	h.handleAddressChanged()
	assert.Equal(t, []string{"healthy", "unhealthy", "healthy"}, log.get())
	assert.False(t, old.IsActive())
	require.NotNil(t, h.pollGroup.Load())
	assert.True(t, h.pollGroup.Load().IsActive())

	// new address unreachable: the host stays drained
	srv.Stop()
	h.handleAddressChanged()
	assert.Equal(t, []string{"healthy", "unhealthy", "healthy", "unhealthy"}, log.get())
	assert.Nil(t, h.pollGroup.Load())
}

func TestHost_AddressChangeResolveError(t *testing.T) {
	e := newEnv(t)
	h := e.newHost("no-port", &healthLog{})

	changed, err := h.isAddressChanged()
	require.Error(t, err)
	assert.False(t, changed)
}

func TestHost_DNSWorkerReconnects(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	_, port, err := net.SplitHostPort(srv.Addr())
	require.NoError(t, err)

	e := newEnv(t, func(c *config.App) { c.DnsCacheTTL = 5 * time.Millisecond })
	log := &healthLog{}
	h := e.newHost(net.JoinHostPort("localhost", port), log)
	require.NoError(t, h.Heat())
	old := h.pollGroup.Load()

	h.mu.Lock()
	h.resolvedAddr = net.JoinHostPort("127.0.0.2", port)
	h.mu.Unlock()

	require.Eventually(t, func() bool {
		return len(log.get()) == 3
	}, waitFor, time.Millisecond)
	assert.Equal(t, []string{"healthy", "unhealthy", "healthy"}, log.get())
	assert.False(t, old.IsActive())
}

func TestHost_InitLostRace(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	e := newEnv(t)
	log := &healthLog{}
	h := e.newHost(srv.Addr(), log)
	require.NoError(t, h.Heat())
	pg := h.pollGroup.Load()

	// a second init (e.g. a health check racing with Heat) must keep the
	// published pollgroup and throw its own one away
	require.NoError(t, h.init())
	assert.Same(t, pg, h.pollGroup.Load())
	assert.True(t, pg.IsActive())
	assert.Equal(t, []string{"healthy"}, log.get())

	// the published pollgroup must still be served by the engine
	e.startEngine()
	cb, ch := callbackChan()
	h.AsyncDo(&types.Request{
		Raw:      &types.ByteBuf{B: []byte("version\r\n")},
		Cmd:      ascii.VersionCmd,
		Callback: cb,
	}, true)
	require.NoError(t, awaitErr(t, ch))
}

func TestHost_DrainTwice(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	e := newEnv(t)
	log := &healthLog{}
	h := e.newHost(srv.Addr(), log)
	require.NoError(t, h.Heat())

	h.drain()
	h.drain()
	assert.Equal(t, []string{"healthy", "unhealthy"}, log.get())
}

// TestHost_HealthWorkerWithRunningEngine reproduces a known data race: the
// host worker enrolls/drains pollgroups (mutating the poller's fd set and the
// pollgroup sockets) while the engine's event loop polls and dispatches them
// on another goroutine. Fixing it needs the engine to perform enrolment and
// draining on its event loop, so the reproducer is opt-in.
func TestHost_HealthWorkerWithRunningEngine(t *testing.T) {
	if os.Getenv("GOMCROUTER_RACE_REPRO") == "" {
		t.Skip("known race between host workers and the engine event loop; set GOMCROUTER_RACE_REPRO=1 and run with -race")
	}

	srv := upstreamtest.NewServer(t)
	e := newEnv(t, func(c *config.App) { c.HealthCheckInterval = 5 * time.Millisecond })
	h := e.newHost(srv.Addr(), &healthLog{})
	require.NoError(t, h.Heat())
	e.startEngine()

	srv.Stop()
	require.Eventually(t, func() bool { return h.pollGroup.Load() == nil }, waitFor, time.Millisecond)
	srv.Restart()
	require.Eventually(t, func() bool { return h.pollGroup.Load() != nil }, waitFor, time.Millisecond)
}
