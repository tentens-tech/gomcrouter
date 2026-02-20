package respond

import (
	"github.com/panjf2000/gnet/v2"
	"github.com/pkg/errors"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/machinery/time"
	"github.com/tentens-tech/gomcrouter/internal/observability/metric"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

func AsyncRespond(resp *types.ByteBuf, req *types.Request) {
	if resp == nil {
		return
	}

	ts := req.Ts

	err := req.Conn.AsyncWrite(resp.B, func(_ gnet.Conn, err error) error {
		pool.BufferPool.Put(resp)
		metric.Collector.HandleRequestAsync(req.Cmd, time.NanoTime()-ts)
		return nil
	})

	// AsyncWrite fires sync error only on internal gnet poller failure.
	// since server becomes unrecoverable - fail-fast.
	if err != nil {
		panic(errors.Wrap(err, "server async write fail"))
	}
}
