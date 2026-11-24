package respond

import (
	"context"
	"sync/atomic"
	"testing"
	stdtime "time"

	"github.com/panjf2000/gnet/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/machinery/time"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

// drainingConn reports an outbound buffer that the test drains; its event
// loop runs every runnable at once and records the close.
type drainingConn struct {
	gnet.Conn
	buffered atomic.Int64
	closed   chan struct{}
}

type drainingLoop struct {
	gnet.EventLoop
	c *drainingConn
}

func (l drainingLoop) Execute(ctx context.Context, r gnet.Runnable) error { return r.Run(ctx) }
func (l drainingLoop) Close(gnet.Conn) error                              { close(l.c.closed); return nil }

func (c *drainingConn) OutboundBuffered() int     { return int(c.buffered.Load()) }
func (c *drainingConn) EventLoop() gnet.EventLoop { return drainingLoop{c: c} }

func shortFlushTimeout(t *testing.T, timeout stdtime.Duration) {
	t.Helper()
	origTimeout, origRetry := closeFlushTimeout, closeFlushRetry
	closeFlushTimeout, closeFlushRetry = int64(timeout), stdtime.Millisecond
	t.Cleanup(func() { closeFlushTimeout, closeFlushRetry = origTimeout, origRetry })
}

func TestCloseWhenFlushed_WaitsForAClientThatKeepsReading(t *testing.T) {
	shortFlushTimeout(t, 50*stdtime.Millisecond)
	c := &drainingConn{closed: make(chan struct{})}
	c.buffered.Store(10)
	closeWhenFlushed(c, &types.ConnState{}, time.NanoTime()+closeFlushTimeout)

	// the client reads a little every 20ms: 200ms in total, 4 times the
	// timeout
	for c.buffered.Load() > 0 {
		select {
		case <-c.closed:
			t.Fatalf("closed with %d bytes buffered", c.buffered.Load())
		case <-stdtime.After(20 * stdtime.Millisecond):
		}
		c.buffered.Add(-1)
	}
	select {
	case <-c.closed:
	case <-stdtime.After(stdtime.Second):
		t.Fatal("not closed after the buffer was flushed")
	}
}

func TestCloseWhenFlushed_ClosesAClientThatStoppedReading(t *testing.T) {
	shortFlushTimeout(t, 50*stdtime.Millisecond)
	c := &drainingConn{closed: make(chan struct{})}
	c.buffered.Store(10)
	start := stdtime.Now()
	closeWhenFlushed(c, &types.ConnState{}, time.NanoTime()+closeFlushTimeout)

	select {
	case <-c.closed:
		assert.GreaterOrEqual(t, stdtime.Since(start), 50*stdtime.Millisecond)
	case <-stdtime.After(stdtime.Second):
		t.Fatal("not closed after the timeout")
	}
	require.EqualValues(t, 10, c.buffered.Load())
}

func TestCloseWhenFlushed_SkipsAClosedConnection(t *testing.T) {
	c := &drainingConn{closed: make(chan struct{})}
	closeWhenFlushed(c, &types.ConnState{Closed: true}, time.NanoTime())
	select {
	case <-c.closed:
		t.Fatal("closed twice")
	default:
	}
}
