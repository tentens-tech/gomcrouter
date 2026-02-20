package handlers

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/server/respond"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream"
	"sync/atomic"
)

func NewAllFastestRouteHandler(pool *upstream.OrderedPool, ctx *config.AppContext) *AllFastestRouteHandler {
	return &AllFastestRouteHandler{
		ctx:  ctx,
		pool: pool,
	}
}

type AllFastestRouteHandler struct {
	pool *upstream.OrderedPool
	ctx  *config.AppContext
}

func (a *AllFastestRouteHandler) Handle(req *types.Request) {
	hosts := a.pool.All()
	if len(hosts) == 0 {
		respond.AsyncRespond(ascii.NoHealthyUpstreamErrorResponseBuf, req)
		pool.BufferPool.Put(req.Raw)
		pool.RequestPool.Put(req)
		return
	}

	var done = atomic.Bool{}
	var numProcessed atomic.Int64
	var initialNum = int64(len(hosts))

	cb := func(resp *types.ByteBuf, err error) {
		cur := numProcessed.Add(1)
		isLast := cur == initialNum

		if isLast {
			defer pool.BufferPool.Put(req.Raw)
			defer pool.RequestPool.Put(req)
		}

		if err != nil {
			a.ctx.Logger.Info("all fastest route: ", err)
			if isLast && !done.Load() {
				respond.AsyncRespond(ascii.ErrProxyErrorResponseBuf, req)
			}
			return
		}

		if ascii.UpstreamRequestFailed(resp.B) {
			if isLast && !done.Load() {
				respond.AsyncRespond(resp, req)
				return
			}

			pool.BufferPool.Put(resp)
			return
		}

		ok := done.CompareAndSwap(false, true)
		if !ok {
			pool.BufferPool.Put(resp)
			return
		}

		respond.AsyncRespond(resp, req)
	}

	req.Callback = cb

	for i, h := range hosts {
		// trigger only on 1st operation to reduce atomic races
		h.AsyncDo(req, i == 0)
	}
}
