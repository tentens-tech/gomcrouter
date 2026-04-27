package handlers

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream"
)

type Handler interface {
	Handle(req *types.Request)
}

// Pool is the minimal contract handlers need from an upstream pool.
// *upstream.OrderedPool satisfies it; tests supply a mock implementation.
type Pool interface {
	All() []upstream.AsyncDoer
}

func NewByPolicy(name config.Policy, pool Pool, ctx *config.AppContext) Handler {
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
