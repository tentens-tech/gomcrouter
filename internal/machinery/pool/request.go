package pool

import (
	"github.com/tentens-tech/gomcrouter/internal/types"
	"sync"
)

var RequestPool = sync.Pool{
	New: func() interface{} {
		return &types.Request{}
	},
}

var InflightRequestPool = sync.Pool{
	New: func() interface{} {
		return &types.InflightRequest{}
	},
}
