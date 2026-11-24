package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/router"
	upio "github.com/tentens-tech/gomcrouter/internal/upstream/io"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const ioTimeout = 5 * time.Second

// fakeMemcached is a tiny in-memory memcached speaking just enough of the
// ASCII protocol for the router: set, get, version.
type fakeMemcached struct {
	ln net.Listener

	// delay is waited before every reply; set it before the first request
	delay time.Duration
	// quits counts the quit commands received
	quits atomic.Int32

	mu    sync.Mutex
	data  map[string]string
	conns []net.Conn
	wg    sync.WaitGroup
}

func startFakeMemcached(t *testing.T) *fakeMemcached {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	m := &fakeMemcached{ln: ln, data: map[string]string{}}
	m.wg.Add(1)
	go m.acceptLoop()
	t.Cleanup(m.close)
	return m
}

func (m *fakeMemcached) addr() string { return m.ln.Addr().String() }

func (m *fakeMemcached) close() {
	_ = m.ln.Close()
	m.mu.Lock()
	for _, c := range m.conns {
		_ = c.Close()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *fakeMemcached) acceptLoop() {
	defer m.wg.Done()
	for {
		c, err := m.ln.Accept()
		if err != nil {
			return
		}
		m.mu.Lock()
		m.conns = append(m.conns, c)
		m.mu.Unlock()
		m.wg.Add(1)
		go m.serve(c)
	}
}

func (m *fakeMemcached) serve(c net.Conn) {
	defer m.wg.Done()
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		var resp string
		switch fields[0] {
		case "quit":
			m.quits.Add(1)
			_ = c.Close()
			return
		case "version":
			resp = "VERSION 1.6.0\r\n"
		case "get":
			var sb strings.Builder
			m.mu.Lock()
			for _, k := range fields[1:] {
				if v, ok := m.data[k]; ok {
					fmt.Fprintf(&sb, "VALUE %s 0 %d\r\n%s\r\n", k, len(v), v)
				}
			}
			m.mu.Unlock()
			resp = sb.String() + "END\r\n"
		case "set":
			n, err := strconv.Atoi(fields[4])
			if err != nil {
				return
			}
			body := make([]byte, n+2)
			if _, err := io.ReadFull(r, body); err != nil {
				return
			}
			m.mu.Lock()
			m.data[fields[1]] = string(body[:n])
			m.mu.Unlock()
			resp = "STORED\r\n"
		default:
			resp = "ERROR\r\n"
		}
		time.Sleep(m.delay)
		if _, err := c.Write([]byte(resp)); err != nil {
			return
		}
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func newTestCtx(t *testing.T, logger *zap.SugaredLogger) *config.AppContext {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &config.AppContext{
		Context: ctx,
		Logger:  logger,
		Config: &config.App{
			Listen:              "tcp://" + freePort(t),
			ServerEventLoops:    1,
			UpstreamConnections: 1,
			TimeoutMillis:       1000,
			TimeoutNs:           int64(time.Second),
			HealthCheckInterval: time.Hour,
			DnsCacheTTL:         time.Hour,
			BufferSize:          64 * 1024,
		},
	}
}

// startRouter starts the full request path: a gnet Server, a Router with the
// given route and upstreams and the upstream io.Engine. Everything is stopped
// on test cleanup. It returns the address clients should dial.
func startRouter(t *testing.T, route config.RouteConfig, upstreams ...string) string {
	t.Helper()
	return startRouterWithLoops(t, 1, route, upstreams...)
}

func startRouterWithLoops(t *testing.T, loops int, route config.RouteConfig, upstreams ...string) string {
	t.Helper()
	return startRouterWith(t, loops, 1, route, upstreams...)
}

func startRouterWith(t *testing.T, loops, upstreamConns int, route config.RouteConfig, upstreams ...string) string {
	t.Helper()
	ctx := newTestCtx(t, zap.NewNop().Sugar())
	ctx.Config.ServerEventLoops = loops
	ctx.Config.UpstreamConnections = upstreamConns

	eng, err := upio.NewEngine(ctx)
	require.NoError(t, err)

	// same order as cmd: upstream hosts are enrolled before the engine loop
	// starts (enrolment is not synchronised with a running loop).
	r := router.New(ctx, config.Router{OrderedPoolConfig: upstreams, RouteConfig: route}, eng)

	engDone := make(chan struct{})
	go func() {
		defer close(engDone)
		eng.Start()
	}()

	srv := NewServer(ctx, r)
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		srv.Serve()
	}()

	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), ioTimeout)
		defer cancel()
		srv.Stop(stopCtx)
		waitDone(t, srvDone, "gnet server")
		eng.Stop()
		waitDone(t, engDone, "upstream engine")
	})

	addr := strings.TrimPrefix(ctx.Config.Listen, "tcp://")
	waitListening(t, addr)
	return addr
}

func waitListening(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, ioTimeout, 5*time.Millisecond)
}

func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(ioTimeout):
		t.Fatalf("%s did not stop", what)
	}
}

type client struct {
	t *testing.T
	c net.Conn
	r *bufio.Reader
}

func dial(t *testing.T, addr string) *client {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, ioTimeout)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.SetDeadline(time.Now().Add(ioTimeout)))
	return &client{t: t, c: c, r: bufio.NewReader(c)}
}

func (c *client) send(s string) {
	c.t.Helper()
	_, err := c.c.Write([]byte(s))
	require.NoError(c.t, err)
}

func (c *client) line() string {
	c.t.Helper()
	l, err := c.r.ReadString('\n')
	require.NoError(c.t, err)
	return l
}

var defaultRoute = config.RouteConfig{Type: string(config.DefaultRoute)}

func TestServer_SetThenGetThroughUpstream(t *testing.T) {
	mc := startFakeMemcached(t)
	cl := dial(t, startRouter(t, defaultRoute, mc.addr()))

	cl.send("get missing\r\n")
	assert.Equal(t, "END\r\n", cl.line())

	cl.send("set k 0 0 5\r\nhello\r\n")
	assert.Equal(t, "STORED\r\n", cl.line())

	cl.send("get k\r\n")
	assert.Equal(t, "VALUE k 0 5\r\n", cl.line())
	assert.Equal(t, "hello\r\n", cl.line())
	assert.Equal(t, "END\r\n", cl.line())
}

func TestServer_PipelinedRequestsKeepOrder(t *testing.T) {
	mc := startFakeMemcached(t)
	cl := dial(t, startRouter(t, defaultRoute, mc.addr()))

	// several requests in a single TCP write: OnTraffic must decode all of them
	cl.send("set a 0 0 1\r\n1\r\nset b 0 0 2\r\n22\r\nget a b\r\nversion\r\n")

	assert.Equal(t, "STORED\r\n", cl.line())
	assert.Equal(t, "STORED\r\n", cl.line())
	assert.Equal(t, "VALUE a 0 1\r\n", cl.line())
	assert.Equal(t, "1\r\n", cl.line())
	assert.Equal(t, "VALUE b 0 2\r\n", cl.line())
	assert.Equal(t, "22\r\n", cl.line())
	assert.Equal(t, "END\r\n", cl.line())
	assert.Equal(t, "VERSION 1.6.0\r\n", cl.line())
}

func TestServer_RequestSplitAcrossWrites(t *testing.T) {
	mc := startFakeMemcached(t)
	cl := dial(t, startRouter(t, defaultRoute, mc.addr()))

	// incomplete header, then incomplete body: the server must wait for the rest
	for _, part := range []string{"set k 0", " 0 5\r\nhe", "llo", "\r\n"} {
		cl.send(part)
		time.Sleep(5 * time.Millisecond) // make it likely each part is a separate read
	}
	assert.Equal(t, "STORED\r\n", cl.line())

	cl.send("get k\r\n")
	assert.Equal(t, "VALUE k 0 5\r\n", cl.line())
	assert.Equal(t, "hello\r\n", cl.line())
	assert.Equal(t, "END\r\n", cl.line())
}

func TestServer_MalformedRequestClosesConnection(t *testing.T) {
	tests := []struct {
		name string
		req  string
	}{
		{name: "unknown command", req: "bogus\r\n"},
		{name: "unknown command with args", req: "bogus k\r\n"},
		{name: "set without length", req: "set k\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := dial(t, startRouter(t, defaultRoute))

			cl.send(tt.req)

			assert.Equal(t, string(ascii.ErrMalformedRequestErrorResponseBuf.B), cl.line())
			_, err := cl.r.ReadByte()
			require.ErrorIs(t, err, io.EOF, "connection must be closed after a malformed request")
		})
	}
}

func TestServer_PipelinedRepliesKeepOrderAcrossHosts(t *testing.T) {
	fast, slow := startFakeMemcached(t), startFakeMemcached(t)
	slow.delay = 100 * time.Millisecond
	fast.data["hit"] = "1"
	slow.data["miss"] = "2"
	route := config.RouteConfig{Type: string(config.MissFailoverRoute)}
	cl := dial(t, startRouter(t, route, fast.addr(), slow.addr()))

	// "get miss" misses on the fast host and is answered late by the slow
	// one, "get hit" is answered at once: the replies must keep the order
	// of the requests anyway
	cl.send("get miss\r\nget hit\r\nversion\r\n")

	assert.Equal(t, "VALUE miss 0 1\r\n", cl.line())
	assert.Equal(t, "2\r\n", cl.line())
	assert.Equal(t, "END\r\n", cl.line())
	assert.Equal(t, "VALUE hit 0 1\r\n", cl.line())
	assert.Equal(t, "1\r\n", cl.line())
	assert.Equal(t, "END\r\n", cl.line())
	assert.Equal(t, "VERSION 1.6.0\r\n", cl.line())
}

func TestServer_PipelinedRepliesKeepOrderAcrossSockets(t *testing.T) {
	mc := startFakeMemcached(t)
	addr := startRouterWith(t, 2, 4, defaultRoute, mc.addr())

	// several clients pipelining at once spread the requests of a
	// connection over the upstream sockets
	const clients, batches, batch = 8, 20, 16
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for c := range clients {
		wg.Go(func() {
			errs <- pipelineRoundTrip(addr, c, batches, batch)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

// pipelineRoundTrip sets batch keys in one write, then reads them back in one
// write, and checks every reply belongs to its request.
func pipelineRoundTrip(addr string, client, batches, batch int) error {
	c, err := net.DialTimeout("tcp", addr, ioTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.SetDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	r := bufio.NewReader(c)

	for b := range batches {
		var sets, gets strings.Builder
		for i := range batch {
			k := fmt.Sprintf("c%d-%d-%d", client, b, i)
			fmt.Fprintf(&sets, "set %s 0 0 %d\r\n%s\r\n", k, len(k), k)
			fmt.Fprintf(&gets, "get %s\r\n", k)
		}
		if _, err := c.Write([]byte(sets.String())); err != nil {
			return err
		}
		for range batch {
			if l, err := r.ReadString('\n'); err != nil || l != "STORED\r\n" {
				return fmt.Errorf("set: %q: %w", l, err)
			}
		}
		if _, err := c.Write([]byte(gets.String())); err != nil {
			return err
		}
		for i := range batch {
			k := fmt.Sprintf("c%d-%d-%d", client, b, i)
			want := fmt.Sprintf("VALUE %s 0 %d\r\n%s\r\nEND\r\n", k, len(k), k)
			got := make([]byte, len(want))
			if _, err := io.ReadFull(r, got); err != nil {
				return err
			}
			if string(got) != want {
				return fmt.Errorf("get %s: got %q", k, got)
			}
		}
	}
	return nil
}

func TestServer_QuitIsAnsweredLocally(t *testing.T) {
	mc := startFakeMemcached(t)
	cl := dial(t, startRouter(t, defaultRoute, mc.addr()))

	// the reply to set comes first, the requests after quit are dropped
	cl.send("set k 0 0 1\r\nx\r\nquit\r\nget k\r\n")

	assert.Equal(t, "STORED\r\n", cl.line())
	_, err := cl.r.ReadByte()
	require.ErrorIs(t, err, io.EOF, "quit must close the connection")
	assert.Zero(t, mc.quits.Load(), "quit must not reach the shared upstream connection")

	// the upstream connection still works for other clients
	other := dial(t, startRouter(t, defaultRoute, mc.addr()))
	other.send("get k\r\n")
	assert.Equal(t, "VALUE k 0 1\r\n", other.line())
}

func TestServer_MalformedRequestAfterPipelinedRequests(t *testing.T) {
	mc := startFakeMemcached(t)
	mc.delay = 50 * time.Millisecond
	cl := dial(t, startRouter(t, defaultRoute, mc.addr()))

	// the error reply waits for the reply to the request before it
	cl.send("set k 0 0 1\r\nx\r\nbogus\r\nget k\r\n")

	assert.Equal(t, "STORED\r\n", cl.line())
	assert.Equal(t, string(ascii.ErrMalformedRequestErrorResponseBuf.B), cl.line())
	_, err := cl.r.ReadByte()
	require.ErrorIs(t, err, io.EOF)
}

func TestServer_NoHealthyUpstream(t *testing.T) {
	cl := dial(t, startRouter(t, defaultRoute, freePort(t)))

	cl.send("get k\r\n")
	assert.Equal(t, string(ascii.NoHealthyUpstreamErrorResponseBuf.B), cl.line())

	// the connection stays usable
	cl.send("set k 0 0 1\r\nx\r\n")
	assert.Equal(t, string(ascii.NoHealthyUpstreamErrorResponseBuf.B), cl.line())
}

func TestServer_LocalRouteAnswersVersion(t *testing.T) {
	route := config.RouteConfig{
		Type:              string(config.OperationPolicySelectorRoute),
		OperationPolicies: map[string]config.Policy{"version": config.LocalRoute},
	}
	cl := dial(t, startRouter(t, route))

	cl.send("version\r\n")
	assert.Equal(t, string(ascii.VersionBuf.B), cl.line())
}

func TestServer_ConcurrentClients(t *testing.T) {
	for _, loops := range []int{1, 2} {
		t.Run(fmt.Sprintf("event loops %d", loops), func(t *testing.T) {
			testConcurrentClients(t, loops)
		})
	}
}

func testConcurrentClients(t *testing.T, loops int) {
	mc := startFakeMemcached(t)
	addr := startRouterWithLoops(t, loops, defaultRoute, mc.addr())

	const clients = 16
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- roundTrip(addr, fmt.Sprintf("key%d", i), fmt.Sprintf("value-%d", i))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

// roundTrip repeatedly sets key=value and reads it back; it reports errors instead of
// failing so it can run outside the test goroutine.
func roundTrip(addr, key, value string) error {
	c, err := net.DialTimeout("tcp", addr, ioTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.SetDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	r := bufio.NewReader(c)

	want := []string{
		"STORED\r\n",
		fmt.Sprintf("VALUE %s 0 %d\r\n", key, len(value)),
		value + "\r\n",
		"END\r\n",
	}
	for round := 0; round < 20; round++ {
		if _, err := fmt.Fprintf(c, "set %s 0 0 %d\r\n%s\r\nget %s\r\n", key, len(value), value, key); err != nil {
			return err
		}
		for _, w := range want {
			l, err := r.ReadString('\n')
			if err != nil {
				return err
			}
			if l != w {
				return fmt.Errorf("round %d: got %q, want %q", round, l, w)
			}
		}
	}
	return nil
}

func TestServer_StopRefusesNewConnections(t *testing.T) {
	ctx := newTestCtx(t, zap.NewNop().Sugar())
	eng, err := upio.NewEngine(ctx)
	require.NoError(t, err)
	srv := NewServer(ctx, router.New(ctx, config.Router{RouteConfig: defaultRoute}, eng))

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Serve()
	}()

	addr := strings.TrimPrefix(ctx.Config.Listen, "tcp://")
	waitListening(t, addr)

	stopCtx, cancel := context.WithTimeout(context.Background(), ioTimeout)
	defer cancel()
	srv.Stop(stopCtx)
	waitDone(t, done, "gnet server")

	_, err = net.DialTimeout("tcp", addr, 100*time.Millisecond)
	require.Error(t, err)
}

func TestServer_ServeFailureIsFatal(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	// replace os.Exit with a panic so the fatal path can be observed
	logger := zap.New(core, zap.WithFatalHook(zapcore.WriteThenPanic)).Sugar()
	ctx := newTestCtx(t, logger)
	ctx.Config.Listen = "bogus://127.0.0.1:0"

	srv := NewServer(ctx, nil)

	require.Panics(t, srv.Serve)
	require.Equal(t, 1, logs.FilterLevelExact(zapcore.FatalLevel).Len())
	assert.Contains(t, logs.FilterLevelExact(zapcore.FatalLevel).All()[0].Message, "failed to start gnet server")
}

func TestServer_StopBeforeServe(t *testing.T) {
	ctx := newTestCtx(t, zap.NewNop().Sugar())
	srv := NewServer(ctx, nil)

	// Stop can run before gnet has booted (e.g. an early shutdown signal);
	// Serve must then return promptly instead of serving forever.
	srv.Stop(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Serve()
	}()
	waitDone(t, done, "gnet server")
}
