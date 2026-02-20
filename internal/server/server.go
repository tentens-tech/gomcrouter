package server

import (
	"context"
	"github.com/panjf2000/gnet/v2"
	"github.com/panjf2000/gnet/v2/pkg/logging"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/machinery/time"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/router"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

type Server struct {
	ctx *config.AppContext

	router *router.Router

	eng gnet.Engine
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

func (s *Server) Stop(ctx context.Context) {
	_ = s.eng.Stop(ctx)
}

func (s *Server) OnBoot(e gnet.Engine) gnet.Action {
	s.eng = e
	return gnet.None
}

func (s *Server) OnTraffic(c gnet.Conn) (action gnet.Action) {
	for {
		buf, _ := c.Peek(-1)
		if len(buf) == 0 {
			return
		}

		raw, cmd, n, err := ascii.DecodeRequest(buf)
		if err != nil {
			s.ctx.Logger.Warnf("failed to decode request: %v", err)
			_, _ = c.Write(ascii.ErrMalformedRequestErrorResponseBuf.B)
			return gnet.Close
		}

		if n == 0 {
			return
		}

		reqByteBuf := pool.BufferPool.Get(n)
		copy(reqByteBuf.B, raw)

		_, _ = c.Discard(n)

		req := pool.RequestPool.Get().(*types.Request)
		req.Raw = reqByteBuf
		req.Cmd = cmd
		req.Conn = c
		req.Ts = time.NanoTime()

		s.router.Handle(req)
	}
}
