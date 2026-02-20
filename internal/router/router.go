package router

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/router/handlers"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream"
	"github.com/tentens-tech/gomcrouter/internal/upstream/io"
)

type Router struct {
	cfg     config.Router
	ctx     *config.AppContext
	pool    *upstream.OrderedPool
	handler handlers.Handler
}

func New(ctx *config.AppContext, routerCfg config.Router, eng *io.Engine) *Router {
	r := &Router{
		ctx: ctx,
		cfg: routerCfg,
	}

	r.pool = upstream.NewOrderedPool(ctx, routerCfg.OrderedPoolConfig, eng)
	if r.cfg.RouteConfig.Type == string(config.OperationPolicySelectorRoute) {
		r.handler = handlers.NewOperationSelectorRouteHandler(r.pool, routerCfg.RouteConfig.OperationPolicies, ctx)
		return r
	}

	r.handler = handlers.NewByPolicy(config.Policy(routerCfg.RouteConfig.Type), r.pool, ctx)

	return r
}

func (r *Router) Handle(req *types.Request) {
	r.handler.Handle(req)
}
