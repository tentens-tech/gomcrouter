package handlers

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/server/respond"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

func NewMissFailoverRouteHandler(pool Pool, ctx *config.AppContext) *MissFailoverRouteHandler {
	return &MissFailoverRouteHandler{
		ctx:  ctx,
		pool: pool,
	}
}

type MissFailoverRouteHandler struct {
	pool Pool
	ctx  *config.AppContext
}

func (m *MissFailoverRouteHandler) Handle(req *types.Request) {
	hosts := m.pool.All()
	if len(hosts) == 0 {
		respond.AsyncRespond(ascii.NoHealthyUpstreamErrorResponseBuf, req)
		pool.BufferPool.Put(req.Raw)
		pool.RequestPool.Put(req)
		return
	}

	var current = 0
	var latestResp = ascii.ErrProxyErrorResponseBuf

	cb := func(resp *types.ByteBuf, err error) {
		if err != nil {
			m.ctx.Logger.Info("miss failover route: ", err)
			if current == len(hosts)-1 {
				respond.AsyncRespond(latestResp, req)
				pool.BufferPool.Put(req.Raw)
				pool.RequestPool.Put(req)
				return
			}
			current++
			hosts[current].AsyncDo(req, true)
			return
		}

		if ascii.UpstreamRequestFailed(resp.B) {
			if current == len(hosts)-1 {
				respond.AsyncRespond(resp, req)
				pool.BufferPool.Put(req.Raw)
				pool.RequestPool.Put(req)
				pool.BufferPool.Put(latestResp)
				return
			}

			pool.BufferPool.Put(latestResp)

			latestResp = resp
			current++
			hosts[current].AsyncDo(req, true)
			return
		}

		respond.AsyncRespond(resp, req)
		pool.BufferPool.Put(req.Raw)
		pool.RequestPool.Put(req)
		pool.BufferPool.Put(latestResp)
	}

	req.Callback = cb

	hosts[current].AsyncDo(req, true)
}
