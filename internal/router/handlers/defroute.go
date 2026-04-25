package handlers

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/server/respond"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

func NewDefRouteHandler(pool Pool, ctx *config.AppContext) *DefRouteHandler {
	return &DefRouteHandler{
		ctx:  ctx,
		pool: pool,
	}
}

type DefRouteHandler struct {
	pool Pool
	ctx  *config.AppContext
}

func (a *DefRouteHandler) Handle(req *types.Request) {
	hosts := a.pool.All()
	if len(hosts) == 0 {
		respond.AsyncRespond(ascii.NoHealthyUpstreamErrorResponseBuf, req)
		pool.BufferPool.Put(req.Raw)
		pool.RequestPool.Put(req)
		return
	}

	cb := func(resp *types.ByteBuf, err error) {
		defer pool.BufferPool.Put(req.Raw)
		defer pool.RequestPool.Put(req)
		if err != nil {
			a.ctx.Logger.Info("default route: ", err)
			respond.AsyncRespond(ascii.ErrProxyErrorResponseBuf, req)
			return
		}

		respond.AsyncRespond(resp, req)
	}

	req.Callback = cb

	hosts[0].AsyncDo(req, true)
}
