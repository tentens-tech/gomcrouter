package handlers

import (
	"errors"
	"testing"

	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
)

func TestDefRoute_NoHosts_RespondsNoHealthyUpstream(t *testing.T) {
	h := NewDefRouteHandler(newMockPool(), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.NoHealthyUpstreamErrorResponseBuf.B))
}

func TestDefRoute_OneHost_Success_RespondsUpstream(t *testing.T) {
	h0 := newMockHost("h0").program(okResp("STORED\r\n"))
	h := NewDefRouteHandler(newMockPool(h0), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "STORED\r\n")
	if h0.calls() != 1 {
		t.Fatalf("expected h0 called once, got %d", h0.calls())
	}
}

func TestDefRoute_OneHost_Error_RespondsProxyError(t *testing.T) {
	h0 := newMockHost("h0").program(errRespOf(errors.New("dial timeout")))
	h := NewDefRouteHandler(newMockPool(h0), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.ErrProxyErrorResponseBuf.B))
}

func TestDefRoute_MultipleHosts_OnlyFirstIsCalled(t *testing.T) {
	h0 := newMockHost("h0").program(okResp("STORED\r\n"))
	h1 := newMockHost("h1") // unprogrammed — would panic if called
	h := NewDefRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "STORED\r\n")
	if h0.calls() != 1 || h1.calls() != 0 {
		t.Fatalf("expected only h0 called: h0=%d h1=%d", h0.calls(), h1.calls())
	}
}

func TestDefRoute_FirstCallTriggersWakeup(t *testing.T) {
	h0 := newMockHost("h0").program(okResp("STORED\r\n"))
	h := NewDefRouteHandler(newMockPool(h0), newTestCtx())

	req, _ := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	if len(h0.triggers) != 1 || !h0.triggers[0] {
		t.Fatalf("expected single triggerWakeup=true, got %v", h0.triggers)
	}
}
