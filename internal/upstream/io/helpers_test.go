package io

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/registry"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream/netpoll"
)

const waitFor = 2 * time.Second

type result struct {
	resp []byte
	err  error
}

// newReq builds a request whose callback forwards a copy of the outcome to
// the returned channel. The channel is buffered so that an unexpected second
// callback does not block the event loop and can be asserted on.
func newReq(cmd int, raw string) (*types.Request, chan result) {
	ch := make(chan result, 4)
	req := &types.Request{
		Raw: &types.ByteBuf{B: []byte(raw)},
		Cmd: cmd,
	}
	req.Callback = func(b *types.ByteBuf, err error) {
		r := result{err: err}
		if b != nil {
			r.resp = append([]byte(nil), b.B...)
		}
		ch <- r
	}
	return req, ch
}

func getReq(key string) (*types.Request, chan result) {
	return newReq(ascii.GetCmd, "get "+key+"\r\n")
}

func await(t *testing.T, ch chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(waitFor):
		require.FailNow(t, "callback was not called in time")
		return result{}
	}
}

func requireNoMore(t *testing.T, ch chan result) {
	t.Helper()
	select {
	case r := <-ch:
		require.FailNow(t, "unexpected extra callback", "%+v", r)
	default:
	}
}

// hostID registers addr in the global host registry; metrics reported by
// the pollgroup look the host name up by id and panic on unknown ids.
func hostID(addr string) int {
	return registry.GlobalHostRegistry.RegisterHost(addr)
}

// newTestEngine creates an engine whose poller is closed at test end. The
// event loop is not started.
func newTestEngine(t *testing.T, ctx *config.AppContext) *Engine {
	t.Helper()
	e, err := NewEngine(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, pg := range append([]*PollGroup(nil), e.pollGroups...) {
			pg.Drain()
		}
		e.poller.Close()
	})
	return e
}

// runEngine starts the event loop in a goroutine and stops it (waiting for
// the loop to exit) at test end. Hosts must be enrolled before calling it:
// the engine does not synchronise enrolment with a running loop.
func runEngine(t *testing.T, e *Engine) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		e.Start()
		close(done)
	}()
	// registered after newTestEngine's cleanup, so it runs first
	t.Cleanup(func() {
		e.Stop()
		select {
		case <-done:
		case <-time.After(waitFor):
			t.Error("event loop did not stop")
		}
	})
}

// newTestPollGroup creates a pollgroup on a private poller, to be driven
// manually with pump. Failure hooks are counted.
type hooks struct {
	failures       int
	pollerFailures []error
}

func newTestPollGroup(t *testing.T, ctx *config.AppContext, addr string) (*PollGroup, *netpoll.Poller, *hooks) {
	t.Helper()
	poller, err := netpoll.NewPoller()
	require.NoError(t, err)

	h := &hooks{}
	pg, err := newPollGroup(addr, hostID(addr), ctx, poller,
		func(err error) { h.pollerFailures = append(h.pollerFailures, err) },
		func() { h.failures++ },
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		pg.Drain()
		poller.Close()
	})
	return pg, poller, h
}

// pump drives the pollgroup on the test goroutine (as the engine's event loop
// would) until cond holds.
func pump(t *testing.T, pg *PollGroup, poller *netpoll.Poller, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for !cond() {
		require.True(t, time.Now().Before(deadline), "condition not met in time")
		if pg.IsActive() {
			pg.dispatch()
		}
		// never block: poll returns at once when no socket event is ready
		require.NoError(t, poller.Poll(0))
		time.Sleep(100 * time.Microsecond)
	}
}

func received(ch chan result, into *result) func() bool {
	return func() bool {
		select {
		case r := <-ch:
			*into = r
			return true
		default:
			return false
		}
	}
}
