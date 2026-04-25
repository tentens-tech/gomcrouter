package handlers

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/panjf2000/gnet/v2"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream"
	"go.uber.org/zap"
)

// fakeConn is a minimal gnet.Conn used by handler tests. It satisfies the
// interface via embedding (the embedded gnet.Conn is a nil interface value;
// calling any method other than AsyncWrite would panic, which is fine — the
// code paths under test only invoke AsyncWrite).
type fakeConn struct {
	gnet.Conn
	mu      sync.Mutex
	written [][]byte
}

func (f *fakeConn) AsyncWrite(buf []byte, callback gnet.AsyncCallback) error {
	f.mu.Lock()
	cp := append([]byte(nil), buf...)
	f.written = append(f.written, cp)
	f.mu.Unlock()
	if callback != nil {
		return callback(nil, nil)
	}
	return nil
}

func (f *fakeConn) writes() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.written))
	copy(out, f.written)
	return out
}

// hostResp is a programmed response for mockHost.AsyncDo to deliver.
type hostResp struct {
	buf *types.ByteBuf
	err error
}

func okResp(s string) hostResp     { return hostResp{buf: bbOf(s)} }
func failedResp(s string) hostResp { return hostResp{buf: bbOf(s)} }
func errRespOf(err error) hostResp { return hostResp{err: err} }

// mockHost implements upstream.AsyncDoer. AsyncDo pops the next programmed
// response and invokes req.Callback synchronously on the calling goroutine.
type mockHost struct {
	name string

	mu        sync.Mutex
	queued    []hostResp
	callCount int
	triggers  []bool
}

func newMockHost(name string) *mockHost {
	return &mockHost{name: name}
}

func (h *mockHost) program(rs ...hostResp) *mockHost {
	h.mu.Lock()
	h.queued = append(h.queued, rs...)
	h.mu.Unlock()
	return h
}

func (h *mockHost) AsyncDo(req *types.Request, triggerWakeup bool) {
	h.mu.Lock()
	if len(h.queued) == 0 {
		name := h.name
		h.mu.Unlock()
		panic("mockHost " + name + ": AsyncDo called without a programmed response")
	}
	r := h.queued[0]
	h.queued = h.queued[1:]
	h.callCount++
	h.triggers = append(h.triggers, triggerWakeup)
	h.mu.Unlock()
	req.Callback(r.buf, r.err)
}

func (h *mockHost) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.callCount
}

// mockPool implements handlers.Pool with a fixed list of AsyncDoers.
type mockPool struct {
	hosts []upstream.AsyncDoer
}

func newMockPool(hs ...upstream.AsyncDoer) *mockPool {
	return &mockPool{hosts: hs}
}

func (p *mockPool) All() []upstream.AsyncDoer {
	return p.hosts
}

// --- common test fixtures ---

func newTestCtx() *config.AppContext {
	return &config.AppContext{
		Context: context.Background(),
		Logger:  zap.NewNop().Sugar(),
		Config:  &config.App{},
	}
}

func newTestRequest(cmd int) (*types.Request, *fakeConn) {
	fc := &fakeConn{}
	req := &types.Request{
		Raw:  &types.ByteBuf{B: []byte("dummy")},
		Cmd:  cmd,
		Ts:   0,
		Conn: fc,
	}
	return req, fc
}

// bbOf builds a non-pool-sized ByteBuf so handlers' BufferPool.Put on it is a no-op.
func bbOf(s string) *types.ByteBuf {
	return &types.ByteBuf{B: []byte(s)}
}

// assertSingleWrite asserts the connection has exactly one async write with the expected body.
func assertSingleWrite(t *testing.T, fc *fakeConn, want string) {
	t.Helper()
	w := fc.writes()
	if len(w) != 1 {
		t.Fatalf("expected exactly 1 write, got %d: %q", len(w), w)
	}
	if string(w[0]) != want {
		t.Fatalf("write mismatch:\n  got:  %q\n  want: %q", w[0], want)
	}
}

func assertNoWrites(t *testing.T, fc *fakeConn) {
	t.Helper()
	w := fc.writes()
	if len(w) != 0 {
		t.Fatalf("expected no writes, got %d: %q", len(w), w)
	}
}

// typeName returns the concrete type name of v (without package prefix), used
// for lightweight assertions on which handler a factory returned.
func typeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}
