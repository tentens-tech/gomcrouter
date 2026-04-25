package handlers

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

func NewOperationSelectorRouteHandler(
	pool Pool,
	opCfg map[string]config.Policy,
	ctx *config.AppContext,
) *OperationSelectorRouteHandler {
	h := &OperationSelectorRouteHandler{
		pool:     pool,
		handlers: make([]Handler, len(ascii.Commands)),
	}

	defHandler := NewDefRouteHandler(pool, ctx)

	for cmdIdx, cmd := range ascii.Commands {
		if policy, ok := opCfg[string(cmd)]; ok {
			h.handlers[cmdIdx] = NewByPolicy(policy, pool, ctx)
			continue
		}

		h.handlers[cmdIdx] = defHandler
	}

	return h
}

type OperationSelectorRouteHandler struct {
	pool Pool

	// handlers represent mapping of cmd -> Handler. handlers always equals length of ascii.Commands.
	// if handler for e.g. set command was declared in configuration, it will be placed on handlers[ascii.SetCmd]
	// otherwise, default handler will be set.
	handlers []Handler
}

func (o *OperationSelectorRouteHandler) Handle(req *types.Request) {
	o.handlers[req.Cmd].Handle(req)
}
