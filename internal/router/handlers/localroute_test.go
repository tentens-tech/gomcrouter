package handlers

import (
	"testing"

	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
)

func TestLocalRoute_Version_RespondsVersionBuf(t *testing.T) {
	// LocalRoute does not touch the pool, but the constructor still requires one.
	h := NewLocalRouteHandler(newMockPool(), newTestCtx())

	req, fc := newTestRequest(ascii.VersionCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.VersionBuf.B))
}

func TestLocalRoute_Quit_NoResponse(t *testing.T) {
	h := NewLocalRouteHandler(newMockPool(), newTestCtx())

	req, fc := newTestRequest(ascii.QuitCmd)
	h.Handle(req)

	assertNoWrites(t, fc)
}

func TestLocalRoute_OtherCmd_RespondsHandlerMismatch(t *testing.T) {
	h := NewLocalRouteHandler(newMockPool(), newTestCtx())

	// Set is not a local command — handler should report a mismatch.
	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.HandlerMismatchBuf.B))
}

func TestLocalRoute_NeverTouchesPool(t *testing.T) {
	// Pool with one mock host that would panic if called: proves LocalRoute
	// short-circuits before consulting the pool.
	hostThatMustNotBeCalled := newMockHost("must-not-be-called")
	h := NewLocalRouteHandler(newMockPool(hostThatMustNotBeCalled), newTestCtx())

	req, _ := newTestRequest(ascii.VersionCmd)
	h.Handle(req)

	if hostThatMustNotBeCalled.calls() != 0 {
		t.Fatalf("local route must not call any host, got %d calls", hostThatMustNotBeCalled.calls())
	}
}
