package io

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/upstream/internal/upstreamtest"
	"golang.org/x/sys/unix"
)

func TestEngine_RoundTrip(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.Set("k", []byte("hello"))
	ctx := upstreamtest.NewAppContext(t)

	e := newTestEngine(t, ctx)
	// a drained group that is still enrolled is skipped by the event loop
	drained, err := e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() {})
	require.NoError(t, err)
	drained.Drain()
	pg, err := e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() {})
	require.NoError(t, err)
	runEngine(t, e)

	req, ch := getReq("k")
	require.True(t, pg.Enqueue(req))
	e.Wakeup()

	r := await(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, "VALUE k 0 5\r\nhello\r\nEND\r\n", string(r.resp))
}

func TestEngine_TimeoutWhileIdle(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetMode(upstreamtest.ModeSilent)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) { c.TimeoutMillis = 50 })

	e := newTestEngine(t, ctx)
	pg, err := e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() {})
	require.NoError(t, err)
	runEngine(t, e)

	req, ch := getReq("k")
	start := time.Now()
	require.True(t, pg.Enqueue(req))
	e.Wakeup()

	r := await(t, ch)
	require.ErrorIs(t, r.err, ErrTimeout)
	assert.Less(t, time.Since(start), time.Second)
}

func TestEngine_ConnectionClosedByServer(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetMode(upstreamtest.ModeClose)
	ctx := upstreamtest.NewAppContext(t)

	e := newTestEngine(t, ctx)
	pg, err := e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() {})
	require.NoError(t, err)
	runEngine(t, e)

	req, ch := getReq("k")
	require.True(t, pg.Enqueue(req))
	e.Wakeup()

	r := await(t, ch)
	require.Error(t, r.err)
	require.Nil(t, r.resp)
}

func TestEngine_PipelinedRequestsKeepOrder(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	const n = 500
	for i := range n {
		srv.Set(fmt.Sprintf("k%d", i), []byte(fmt.Sprintf("v%d", i)))
	}
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) { c.UpstreamConnections = 3 })

	e := newTestEngine(t, ctx)
	pg, err := e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() {})
	require.NoError(t, err)
	require.Equal(t, 3, pg.NumSockets())
	runEngine(t, e)

	chans := make([]chan result, n)
	for i := range n {
		req, ch := getReq(fmt.Sprintf("k%d", i))
		chans[i] = ch
		require.True(t, pg.Enqueue(req))
		e.Wakeup()
	}

	for i, ch := range chans {
		r := await(t, ch)
		require.NoError(t, r.err)
		require.Equal(t, fmt.Sprintf("VALUE k%d 0 %d\r\nv%d\r\nEND\r\n", i, len(fmt.Sprint(i))+1, i), string(r.resp))
	}

	set, ch := newReq(ascii.SetCmd, "set x 0 0 3\r\nabc\r\n")
	require.True(t, pg.Enqueue(set))
	e.Wakeup()
	r := await(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, "STORED\r\n", string(r.resp))
}

func TestEngine_LateResponseAfterTimeoutIsDropped(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.Set("k", []byte("v"))
	const delay = 100 * time.Millisecond
	srv.SetDelay(delay)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) {
		c.TimeoutMillis = 30
		c.UpstreamConnections = 1
	})

	e := newTestEngine(t, ctx)
	pg, err := e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() {})
	require.NoError(t, err)
	runEngine(t, e)

	req, ch := getReq("k")
	start := time.Now()
	require.True(t, pg.Enqueue(req))
	e.Wakeup()
	require.ErrorIs(t, await(t, ch).err, ErrTimeout)

	// let the late answer arrive before the next request is sent
	time.Sleep(time.Until(start.Add(delay + 30*time.Millisecond)))

	// the late answer to the cancelled request must be consumed silently and
	// must not shift the responses of the following requests.
	srv.SetDelay(0)
	req2, ch2 := newReq(ascii.VersionCmd, "version\r\n")
	require.True(t, pg.Enqueue(req2))
	e.Wakeup()

	r := await(t, ch2)
	require.NoError(t, r.err)
	assert.Equal(t, "VERSION 1.6.0-fake\r\n", string(r.resp))
	requireNoMore(t, ch)
	assert.Equal(t, 1, srv.Accepted(), "the connection must be kept")
}

func TestEngine_ReconnectsAfterServerClosedConnection(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetMode(upstreamtest.ModeClose)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) { c.UpstreamConnections = 1 })

	failures := 0
	e := newTestEngine(t, ctx)
	pg, err := e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() { failures++ })
	require.NoError(t, err)
	runEngine(t, e)

	req, ch := getReq("k")
	require.True(t, pg.Enqueue(req))
	e.Wakeup()

	r := await(t, ch)
	require.Error(t, r.err, "a lost connection must never be reported as success")
	assert.Nil(t, r.resp)

	srv.SetMode(upstreamtest.ModeNormal)
	require.Eventually(t, func() bool { return srv.Accepted() == 2 }, waitFor, time.Millisecond)

	req, ch = getReq("k")
	require.True(t, pg.Enqueue(req))
	e.Wakeup()
	r = await(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, "END\r\n", string(r.resp))
	assert.True(t, pg.IsActive())
}

func TestEngine_EnrollHostRefused(t *testing.T) {
	ctx := upstreamtest.NewAppContext(t)
	e := newTestEngine(t, ctx)

	addr := upstreamtest.RefusedAddr(t)
	pg, err := e.EnrollHost(addr, hostID(addr), func() {})
	require.ErrorIs(t, err, unix.ECONNREFUSED)
	assert.Nil(t, pg)
	assert.Empty(t, e.pollGroups)
}

func TestEngine_DisenrollHostRemovesOnlyThatPollGroup(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) { c.UpstreamConnections = 1 })
	e := newTestEngine(t, ctx)

	// two pollgroups to the same address (two hosts resolving to the same
	// ip:port, or Host.init losing its enrolment race)
	first, err := e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() {})
	require.NoError(t, err)
	second, err := e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() {})
	require.NoError(t, err)

	second.Drain()
	e.DisenrollHost(second)

	require.Len(t, e.pollGroups, 1)
	require.Same(t, first, e.pollGroups[0])

	// unknown pollgroup: no-op
	e.DisenrollHost(second)
	require.Len(t, e.pollGroups, 1)
	require.Same(t, first, e.pollGroups[0])
}

func TestEngine_PollerFailureFailsEveryHostAndRecreatesPoller(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) { c.UpstreamConnections = 1 })
	e := newTestEngine(t, ctx)

	// hooks behave like upstream.Host.drain: drain and disenroll the group
	const hosts = 3
	calls := make([]int, hosts)
	for i := range hosts {
		var pg *PollGroup
		var err error
		pg, err = e.EnrollHost(srv.Addr(), hostID(srv.Addr()), func() {
			calls[i]++
			pg.Drain()
			e.DisenrollHost(pg)
		})
		require.NoError(t, err)
	}

	oldPoller := e.poller
	e.onPollerFailure(errors.New("boom"))

	assert.Equal(t, []int{1, 1, 1}, calls, "every host must be failed exactly once")
	assert.Empty(t, e.pollGroups)
	assert.NotSame(t, oldPoller, e.poller)
	assert.False(t, e.maint.Load())
	assert.True(t, e.open.Load())

	// a poller failure while another recovery is in progress is ignored
	e.maint.Store(true)
	cur := e.poller
	e.recreatePoller()
	assert.Same(t, cur, e.poller)
	e.maint.Store(false)
}

func TestEngine_WakeupFailureRecreatesPoller(t *testing.T) {
	ctx := upstreamtest.NewAppContext(t)
	e := newTestEngine(t, ctx)

	old := e.poller
	old.Close()
	e.Wakeup() // the wakeup pipe is closed: EBADF

	assert.NotSame(t, old, e.poller)
	require.NoError(t, e.poller.Wakeup())
}

func TestEngine_ScrapeConnectionCount(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) { c.UpstreamConnections = 3 })
	e := newTestEngine(t, ctx)

	label := t.Name() + "/" + srv.Addr()
	_, err := e.EnrollHost(srv.Addr(), hostID(label), func() {})
	require.NoError(t, err)

	e.scrapeHostConnectionCountMetric()

	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	var got []float64
	for _, mf := range mfs {
		if mf.GetName() != "gomcrouter_upstream_connections_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "host" && l.GetValue() == label {
					got = append(got, m.GetGauge().GetValue())
				}
			}
		}
	}
	assert.Equal(t, []float64{3}, got)
}
