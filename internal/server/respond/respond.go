package respond

import (
	"context"
	stdtime "time"

	"github.com/panjf2000/gnet/v2"
	"github.com/pkg/errors"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/machinery/time"
	"github.com/tentens-tech/gomcrouter/internal/observability/metric"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

// AsyncRespond sends resp as the reply to req and returns resp to the buffer
// pool once it's written. Replies are written in the order of the requests on
// the connection (see types.ConnState). A nil resp sends nothing but lets the
// replies to the next requests go.
//
// req is read before AsyncRespond returns, so it can go back to the request
// pool right after the call (not before it).
func AsyncRespond(resp *types.ByteBuf, req *types.Request) {
	reply(req.Conn, req.State, req.Seq, types.Reply{Resp: resp, Cmd: req.Cmd, Ts: req.Ts})
}

// AsyncRespondAndClose takes the next position on the connection, writes
// resp (if not nil) after the replies to the earlier requests and closes the
// connection. It must be called on the event loop of the connection.
func AsyncRespondAndClose(conn gnet.Conn, st *types.ConnState, resp *types.ByteBuf) {
	var seq uint64
	if st != nil {
		seq = st.NextSeq
		st.NextSeq++
		st.Quit = true
	}
	reply(conn, st, seq, types.Reply{Resp: resp, Close: true})
}

// A closing connection waits for the client to read the queued replies,
// checking every closeFlushRetry; it is closed anyway when the client reads
// nothing for closeFlushTimeout.
var (
	closeFlushTimeout = int64(30 * stdtime.Second)
	closeFlushRetry   = 5 * stdtime.Millisecond
	// closeConn is replaced in tests that use a fake connection
	closeConn = closeWhenFlushed
)

func reply(conn gnet.Conn, st *types.ConnState, seq uint64, r types.Reply) {
	if st == nil {
		write(conn, nil, r)
		return
	}

	st.Mu.Lock()
	defer st.Mu.Unlock()

	if seq != st.Written {
		if st.Pending == nil {
			st.Pending = make(map[uint64]types.Reply)
		}
		st.Pending[seq] = r
		return
	}

	write(conn, st, r)
	st.Written++
	for {
		next, ok := st.Pending[st.Written]
		if !ok {
			return
		}
		delete(st.Pending, st.Written)
		write(conn, st, next)
		st.Written++
	}
}

// write queues the reply on the event loop of the connection. gnet runs the
// queued writes before a queued close, in the order they were queued.
func write(conn gnet.Conn, st *types.ConnState, r types.Reply) {
	if r.Resp != nil {
		resp, cmd, ts, report := r.Resp, r.Cmd, r.Ts, !r.Close
		err := conn.AsyncWrite(resp.B, func(_ gnet.Conn, _ error) error {
			pool.BufferPool.Put(resp)
			if report {
				metric.Collector.HandleRequestAsync(cmd, time.NanoTime()-ts)
			}
			return nil
		})

		// AsyncWrite fires sync error only on internal gnet poller failure.
		// since server becomes unrecoverable - fail-fast.
		if err != nil {
			panic(errors.Wrap(err, "server async write fail"))
		}
	}

	if r.Close {
		if st == nil {
			_ = conn.Close()
			return
		}
		closeConn(conn, st, time.NanoTime()+closeFlushTimeout)
	}
}

// closeWhenFlushed closes the connection once gnet has sent everything queued
// for it: gnet's own close writes the rest of the outbound buffer only while
// the socket accepts it, so a client reading slowly would lose the end of the
// replies. The check runs on the event loop after the queued writes.
func closeWhenFlushed(conn gnet.Conn, st *types.ConnState, deadline int64) {
	waitFlushed(conn, st, deadline, -1)
}

// waitFlushed closes the connection once its outbound buffer is empty. The
// deadline moves forward while the client reads (the buffer shrinks), so it
// only cuts off a client that stopped reading.
func waitFlushed(conn gnet.Conn, st *types.ConnState, deadline int64, last int) {
	check := gnet.RunnableFunc(func(context.Context) error {
		if st.Closed {
			return nil
		}
		buffered := conn.OutboundBuffered()
		now := time.NanoTime()
		if last >= 0 && buffered < last {
			deadline = now + closeFlushTimeout
		}
		if buffered > 0 && now < deadline {
			stdtime.AfterFunc(closeFlushRetry, func() { waitFlushed(conn, st, deadline, buffered) })
			return nil
		}
		_ = conn.EventLoop().Close(conn)
		return nil
	})
	if err := conn.EventLoop().Execute(context.Background(), check); err != nil {
		// the engine is shutting down and closes every connection
		_ = conn.Close()
	}
}
