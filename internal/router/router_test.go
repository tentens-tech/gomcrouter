package router

import (
	"context"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/panjf2000/gnet/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream/io"
	"go.uber.org/zap"
)

// fakeConn records AsyncWrite calls; every other gnet.Conn method panics
// (the embedded interface is nil), which is fine for the routes under test.
type fakeConn struct {
	gnet.Conn
	mu      sync.Mutex
	written []string
}

func (f *fakeConn) AsyncWrite(buf []byte, cb gnet.AsyncCallback) error {
	f.mu.Lock()
	f.written = append(f.written, string(buf))
	f.mu.Unlock()
	if cb != nil {
		return cb(f, nil)
	}
	return nil
}

func (f *fakeConn) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.written...)
}

func newTestCtx(t *testing.T) *config.AppContext {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	// stops the host workers started by upstream.NewHost
	t.Cleanup(cancel)
	return &config.AppContext{
		Context: ctx,
		Logger:  zap.NewNop().Sugar(),
		Config: &config.App{
			UpstreamConnections: 1,
			TimeoutMillis:       500,
			TimeoutNs:           int64(500 * time.Millisecond),
			HealthCheckInterval: time.Hour,
			DnsCacheTTL:         time.Hour,
			BufferSize:          4096,
		},
	}
}

func newTestEngine(t *testing.T, ctx *config.AppContext) *io.Engine {
	t.Helper()
	eng, err := io.NewEngine(ctx)
	require.NoError(t, err)
	return eng
}

func newRequest(cmd int, raw string) (*types.Request, *fakeConn) {
	fc := &fakeConn{}
	buf := pool.BufferPool.Get(len(raw))
	copy(buf.B, raw)
	return &types.Request{Raw: buf, Cmd: cmd, Conn: fc}, fc
}

func handlerName(h any) string {
	t := reflect.TypeOf(h)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

func TestNew_SelectsHandlerByRouteType(t *testing.T) {
	tests := []struct {
		routeType string
		want      string
	}{
		{routeType: "", want: "DefRouteHandler"},
		{routeType: string(config.DefaultRoute), want: "DefRouteHandler"},
		{routeType: "NoSuchRoute", want: "DefRouteHandler"},
		{routeType: string(config.AllFastestRoute), want: "AllFastestRouteHandler"},
		{routeType: string(config.MissFailoverRoute), want: "MissFailoverRouteHandler"},
		{routeType: string(config.LocalRoute), want: "LocalRouteHandler"},
		{routeType: string(config.OperationPolicySelectorRoute), want: "OperationSelectorRouteHandler"},
	}
	for _, tt := range tests {
		t.Run(tt.routeType, func(t *testing.T) {
			ctx := newTestCtx(t)
			cfg := config.Router{RouteConfig: config.RouteConfig{Type: tt.routeType}}

			r := New(ctx, cfg, newTestEngine(t, ctx))

			require.NotNil(t, r.pool)
			assert.Equal(t, tt.want, handlerName(r.handler))
			assert.Equal(t, cfg, r.cfg)
			assert.Same(t, ctx, r.ctx)
		})
	}
}

func TestHandle_DispatchesToConfiguredRoute(t *testing.T) {
	noUpstream := string(ascii.NoHealthyUpstreamErrorResponseBuf.B)
	version := string(ascii.VersionBuf.B)

	tests := []struct {
		name  string
		route config.RouteConfig
		cmd   int
		raw   string
		want  []string
	}{
		{
			name:  "default route without upstreams",
			route: config.RouteConfig{Type: string(config.DefaultRoute)},
			cmd:   ascii.GetCmd,
			raw:   "get k\r\n",
			want:  []string{noUpstream},
		},
		{
			name:  "local route answers version itself",
			route: config.RouteConfig{Type: string(config.LocalRoute)},
			cmd:   ascii.VersionCmd,
			raw:   "version\r\n",
			want:  []string{version},
		},
		{
			name:  "local route rejects data commands",
			route: config.RouteConfig{Type: string(config.LocalRoute)},
			cmd:   ascii.GetCmd,
			raw:   "get k\r\n",
			want:  []string{string(ascii.HandlerMismatchBuf.B)},
		},
		{
			name: "operation selector uses the per-command policy",
			route: config.RouteConfig{
				Type:              string(config.OperationPolicySelectorRoute),
				OperationPolicies: map[string]config.Policy{"version": config.LocalRoute},
			},
			cmd:  ascii.VersionCmd,
			raw:  "version\r\n",
			want: []string{version},
		},
		{
			name: "operation selector falls back to the default route",
			route: config.RouteConfig{
				Type:              string(config.OperationPolicySelectorRoute),
				OperationPolicies: map[string]config.Policy{"version": config.LocalRoute},
			},
			cmd:  ascii.GetCmd,
			raw:  "get k\r\n",
			want: []string{noUpstream},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestCtx(t)
			r := New(ctx, config.Router{RouteConfig: tt.route}, newTestEngine(t, ctx))

			req, fc := newRequest(tt.cmd, tt.raw)
			r.Handle(req)

			assert.Equal(t, tt.want, fc.writes())
		})
	}
}

// acceptAll starts a TCP listener that accepts and holds connections, which
// is enough for an upstream host to be considered healthy at startup.
func acceptAll(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String()
}

// closedAddr returns an address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestNew_BuildsOrderedPoolFromConfig(t *testing.T) {
	ctx := newTestCtx(t)
	healthy := acceptAll(t)

	cfg := config.Router{
		OrderedPoolConfig: config.OrderedPoolConfig{closedAddr(t), healthy},
		RouteConfig:       config.RouteConfig{Type: string(config.DefaultRoute)},
	}

	r := New(ctx, cfg, newTestEngine(t, ctx))

	// only the reachable upstream is in the healthy set
	require.Len(t, r.pool.All(), 1)
}
