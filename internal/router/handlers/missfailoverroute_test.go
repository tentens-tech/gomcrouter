package handlers

import (
	"errors"
	"testing"

	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
)

func TestMissFailover_NoHosts_RespondsNoHealthyUpstream(t *testing.T) {
	h := NewMissFailoverRouteHandler(newMockPool(), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.NoHealthyUpstreamErrorResponseBuf.B))
}

func TestMissFailover_OneHost_Success_RespondsUpstream(t *testing.T) {
	h0 := newMockHost("h0").program(okResp("VALUE foo 0 3\r\nbar\r\nEND\r\n"))
	h := NewMissFailoverRouteHandler(newMockPool(h0), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "VALUE foo 0 3\r\nbar\r\nEND\r\n")
}

func TestMissFailover_OneHost_Miss_PassesThrough(t *testing.T) {
	// Single host: bare END (miss) is the final answer, forwarded as-is.
	h0 := newMockHost("h0").program(failedResp(string(ascii.EndResponse)))
	h := NewMissFailoverRouteHandler(newMockPool(h0), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.EndResponse))
}

func TestMissFailover_OneHost_Error_RespondsProxyError(t *testing.T) {
	// On error, the latest "failed" response stays at the default
	// ErrProxyErrorResponseBuf and is forwarded.
	h0 := newMockHost("h0").program(errRespOf(errors.New("dial timeout")))
	h := NewMissFailoverRouteHandler(newMockPool(h0), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.ErrProxyErrorResponseBuf.B))
}

func TestMissFailover_TwoHosts_FirstHit_DoesNotConsultSecond(t *testing.T) {
	h0 := newMockHost("h0").program(okResp("VALUE k 0 1\r\nv\r\nEND\r\n"))
	h1 := newMockHost("h1") // unprogrammed; a call would panic
	h := NewMissFailoverRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "VALUE k 0 1\r\nv\r\nEND\r\n")
	if h0.calls() != 1 || h1.calls() != 0 {
		t.Fatalf("expected only h0 to be queried: h0=%d h1=%d", h0.calls(), h1.calls())
	}
}

func TestMissFailover_TwoHosts_FirstMissSecondHit_RespondsSecond(t *testing.T) {
	h0 := newMockHost("h0").program(failedResp(string(ascii.EndResponse)))
	h1 := newMockHost("h1").program(okResp("VALUE k 0 1\r\nv\r\nEND\r\n"))
	h := NewMissFailoverRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "VALUE k 0 1\r\nv\r\nEND\r\n")
	if h0.calls() != 1 || h1.calls() != 1 {
		t.Fatalf("expected both hosts to be queried: h0=%d h1=%d", h0.calls(), h1.calls())
	}
}

func TestMissFailover_TwoHosts_FirstErrorSecondHit_RespondsSecond(t *testing.T) {
	h0 := newMockHost("h0").program(errRespOf(errors.New("e0")))
	h1 := newMockHost("h1").program(okResp("VALUE k 0 1\r\nv\r\nEND\r\n"))
	h := NewMissFailoverRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "VALUE k 0 1\r\nv\r\nEND\r\n")
}

func TestMissFailover_TwoHosts_BothMiss_RespondsLastMiss(t *testing.T) {
	h0 := newMockHost("h0").program(failedResp(string(ascii.EndResponse)))
	h1 := newMockHost("h1").program(failedResp(string(ascii.EndResponse)))
	h := NewMissFailoverRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	// Last host's failed response is forwarded.
	assertSingleWrite(t, fc, string(ascii.EndResponse))
}

func TestMissFailover_TwoHosts_BothError_RespondsProxyError(t *testing.T) {
	h0 := newMockHost("h0").program(errRespOf(errors.New("e0")))
	h1 := newMockHost("h1").program(errRespOf(errors.New("e1")))
	h := NewMissFailoverRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.ErrProxyErrorResponseBuf.B))
}

func TestMissFailover_TwoHosts_FirstMissSecondError_RespondsFirstMiss(t *testing.T) {
	// After the first miss, latestResp is updated to that miss; the second
	// host errors out, so the handler emits the most recent non-error
	// failed response (the miss from h0).
	h0 := newMockHost("h0").program(failedResp(string(ascii.EndResponse)))
	h1 := newMockHost("h1").program(errRespOf(errors.New("e1")))
	h := NewMissFailoverRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.EndResponse))
}

func TestMissFailover_AllCallsTriggerWakeup(t *testing.T) {
	h0 := newMockHost("h0").program(failedResp(string(ascii.EndResponse)))
	h1 := newMockHost("h1").program(okResp("VALUE k 0 1\r\nv\r\nEND\r\n"))
	h := NewMissFailoverRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, _ := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	if len(h0.triggers) != 1 || !h0.triggers[0] {
		t.Fatalf("h0 should be triggered with wakeup=true, got %v", h0.triggers)
	}
	if len(h1.triggers) != 1 || !h1.triggers[0] {
		t.Fatalf("h1 should be triggered with wakeup=true, got %v", h1.triggers)
	}
}
