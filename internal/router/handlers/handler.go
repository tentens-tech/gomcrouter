package handlers

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream"
)

type Handler interface {
	Handle(req *types.Request)
}

func NewByPolicy(name config.Policy, pool *upstream.OrderedPool, ctx *config.AppContext) Handler {
	switch name {
	case config.AllFastestRoute:
		return NewAllFastestRouteHandler(pool, ctx)
	case config.MissFailoverRoute:
		return NewMissFailoverRouteHandler(pool, ctx)
	case config.LocalRoute:
		return NewLocalRouteHandler(pool, ctx)
	case config.DefaultRoute:
		return NewDefRouteHandler(pool, ctx)
	default:
		return NewDefRouteHandler(pool, ctx)
	}
}
