package respond

import (
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

func reply(conn gnet.Conn, st *types.ConnState, seq uint64, r types.Reply) {
	if st == nil {
		write(conn, r)
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

	write(conn, r)
	st.Written++
	for {
		next, ok := st.Pending[st.Written]
		if !ok {
			return
		}
		delete(st.Pending, st.Written)
		write(conn, next)
		st.Written++
	}
}

// write queues the reply on the event loop of the connection. gnet runs the
// queued writes before a queued close, in the order they were queued.
func write(conn gnet.Conn, r types.Reply) {
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
		_ = conn.Close()
	}
}
