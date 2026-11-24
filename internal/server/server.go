package server

import (
	"context"
	"sync"

	"github.com/panjf2000/gnet/v2"
	"github.com/panjf2000/gnet/v2/pkg/logging"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/machinery/time"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/router"
	"github.com/tentens-tech/gomcrouter/internal/server/respond"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

type Server struct {
	ctx *config.AppContext

	router *router.Router

	// mu guards eng and stopped: OnBoot runs on the Serve goroutine while
	// Stop is called from the shutdown goroutine.
	mu      sync.Mutex
	eng     gnet.Engine
	booted  bool
	stopped bool

	gnet.BuiltinEventEngine
}

func NewServer(ctx *config.AppContext, router *router.Router) *Server {
	return &Server{
		ctx:    ctx,
		router: router,
	}
}

func (s *Server) Serve() {
	err := gnet.Run(s,
		s.ctx.Config.Listen,
		gnet.WithMulticore(true),
		gnet.WithReusePort(true),
		gnet.WithTCPNoDelay(gnet.TCPNoDelay),
		gnet.WithNumEventLoop(s.ctx.Config.ServerEventLoops),
		gnet.WithLogger(s.ctx.Logger),
		gnet.WithLogLevel(logging.ErrorLevel))
	if err != nil {
		s.ctx.Logger.Fatalf("failed to start gnet server: %v", err)
	}
}

// Stop shuts the engine down and waits (bounded by ctx) until it has
// stopped. If the engine has not booted yet, it is told to shut down as soon
// as it boots, so Serve returns instead of running forever.
func (s *Server) Stop(ctx context.Context) {
	s.mu.Lock()
	s.stopped = true
	eng, booted := s.eng, s.booted
	s.mu.Unlock()

	if booted {
		_ = eng.Stop(ctx)
	}
}

func (s *Server) OnBoot(e gnet.Engine) gnet.Action {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return gnet.Shutdown
	}

	s.eng = e
	s.booted = true
	return gnet.None
}

// OnOpen attaches the state that orders the replies of the connection.
func (s *Server) OnOpen(c gnet.Conn) ([]byte, gnet.Action) {
	c.SetContext(&types.ConnState{})
	return nil, gnet.None
}

func (s *Server) OnTraffic(c gnet.Conn) (action gnet.Action) {
	st := c.Context().(*types.ConnState) //nolint:errcheck // set in OnOpen

	for {
		buf, _ := c.Peek(-1)
		if len(buf) == 0 {
			return
		}

		// the connection is closing: drop what the client sent after quit
		if st.Quit {
			_, _ = c.Discard(len(buf))
			return
		}

		raw, cmd, n, err := ascii.DecodeRequest(buf)
		if err != nil {
			s.ctx.Logger.Warnf("failed to decode request: %v", err)
			_, _ = c.Discard(len(buf))
			respond.AsyncRespondAndClose(c, st, ascii.ErrMalformedRequestErrorResponseBuf)
			return
		}

		if n == 0 {
			return
		}

		// quit is answered by the router whatever the route: memcached
		// would close the upstream connection that other clients share.
		if cmd == ascii.QuitCmd {
			_, _ = c.Discard(len(buf))
			respond.AsyncRespondAndClose(c, st, nil)
			return
		}

		reqByteBuf := pool.BufferPool.Get(n)
		copy(reqByteBuf.B, raw)

		_, _ = c.Discard(n)

		req := pool.RequestPool.Get().(*types.Request) //nolint:errcheck // the pool only holds *types.Request
		req.Raw = reqByteBuf
		req.Cmd = cmd
		req.Conn = c
		req.Ts = time.NanoTime()
		req.State = st
		req.Seq = st.NextSeq
		st.NextSeq++

		s.router.Handle(req)
	}
}
