package handlers

import (
	"errors"
	"testing"

	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
)

func TestAllFastest_NoHosts_RespondsNoHealthyUpstream(t *testing.T) {
	h := NewAllFastestRouteHandler(newMockPool(), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.NoHealthyUpstreamErrorResponseBuf.B))
}

func TestAllFastest_OneHost_Success(t *testing.T) {
	h0 := newMockHost("h0").program(okResp("STORED\r\n"))
	h := NewAllFastestRouteHandler(newMockPool(h0), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "STORED\r\n")
}

func TestAllFastest_OneHost_Error_RespondsProxyError(t *testing.T) {
	h0 := newMockHost("h0").program(errRespOf(errors.New("io error")))
	h := NewAllFastestRouteHandler(newMockPool(h0), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.ErrProxyErrorResponseBuf.B))
}

func TestAllFastest_OneHost_FailedResponse_PassesThrough(t *testing.T) {
	// NOT_FOUND is treated as a "failed" upstream response; with a single
	// host the handler should still surface that response back to the client.
	h0 := newMockHost("h0").program(failedResp(string(ascii.NotFoundResponse)))
	h := NewAllFastestRouteHandler(newMockPool(h0), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.NotFoundResponse))
}

func TestAllFastest_TwoHosts_FirstSuccessWins(t *testing.T) {
	// Mock callbacks fire synchronously in iteration order, so h0 wins the
	// done CAS and its body is what the client should see.
	h0 := newMockHost("h0").program(okResp("STORED\r\n"))
	h1 := newMockHost("h1").program(okResp("STORED\r\n"))
	h := NewAllFastestRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "STORED\r\n")
	if h0.calls() != 1 || h1.calls() != 1 {
		t.Fatalf("expected fan-out to both hosts: h0=%d h1=%d", h0.calls(), h1.calls())
	}
}

func TestAllFastest_TwoHosts_FanoutTriggerOnlyOnFirst(t *testing.T) {
	h0 := newMockHost("h0").program(okResp("STORED\r\n"))
	h1 := newMockHost("h1").program(okResp("STORED\r\n"))
	h := NewAllFastestRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, _ := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	if len(h0.triggers) != 1 || !h0.triggers[0] {
		t.Fatalf("h0 should be triggered with wakeup=true, got %v", h0.triggers)
	}
	if len(h1.triggers) != 1 || h1.triggers[0] {
		t.Fatalf("h1 should be triggered with wakeup=false, got %v", h1.triggers)
	}
}

func TestAllFastest_TwoHosts_BothError_RespondsProxyError(t *testing.T) {
	h0 := newMockHost("h0").program(errRespOf(errors.New("e0")))
	h1 := newMockHost("h1").program(errRespOf(errors.New("e1")))
	h := NewAllFastestRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.ErrProxyErrorResponseBuf.B))
}

func TestAllFastest_TwoHosts_BothFailed_RespondsLastFailed(t *testing.T) {
	h0 := newMockHost("h0").program(failedResp(string(ascii.NotFoundResponse)))
	h1 := newMockHost("h1").program(failedResp(string(ascii.ExistsResponse)))
	h := NewAllFastestRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.GetCmd)
	h.Handle(req)

	// Only the last failed response is forwarded to the client.
	assertSingleWrite(t, fc, string(ascii.ExistsResponse))
}

func TestAllFastest_TwoHosts_FirstErrorThenSuccess_RespondsSuccess(t *testing.T) {
	h0 := newMockHost("h0").program(errRespOf(errors.New("e0")))
	h1 := newMockHost("h1").program(okResp("STORED\r\n"))
	h := NewAllFastestRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "STORED\r\n")
}

func TestAllFastest_TwoHosts_FirstSuccessThenError_RespondsSuccessOnly(t *testing.T) {
	h0 := newMockHost("h0").program(okResp("STORED\r\n"))
	h1 := newMockHost("h1").program(errRespOf(errors.New("e1")))
	h := NewAllFastestRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	// Single response, the early success — the later error is swallowed
	// because done was already set.
	assertSingleWrite(t, fc, "STORED\r\n")
}

func TestAllFastest_TwoHosts_FirstFailedThenSuccess_RespondsSuccess(t *testing.T) {
	h0 := newMockHost("h0").program(failedResp(string(ascii.NotFoundResponse)))
	h1 := newMockHost("h1").program(okResp("STORED\r\n"))
	h := NewAllFastestRouteHandler(newMockPool(h0, h1), newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "STORED\r\n")
}
