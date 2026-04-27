package handlers

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/server/respond"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

func NewLocalRouteHandler(pool Pool, ctx *config.AppContext) *LocalRouteHandler {
	return &LocalRouteHandler{
		ctx:  ctx,
		pool: pool,
	}
}

type LocalRouteHandler struct {
	pool Pool
	ctx  *config.AppContext
}

func (a *LocalRouteHandler) Handle(req *types.Request) {
	pool.BufferPool.Put(req.Raw)
	pool.RequestPool.Put(req)

	switch req.Cmd {
	case ascii.VersionCmd:
		respond.AsyncRespond(ascii.VersionBuf, req)
	case ascii.QuitCmd:
		// no response on quit command
		return
	default:
		respond.AsyncRespond(ascii.HandlerMismatchBuf, req)
	}
}
