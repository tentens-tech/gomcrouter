package handlers

import (
	"testing"

	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
)

// The operation selector dispatches by command index. Tests below exercise
// distinct policies (LocalRoute, DefaultRoute, AllFastestRoute) and verify the
// expected sub-handler ran by inspecting which mock hosts were called and
// what was written back to the client.

func TestOperationSelector_DispatchesLocalRoute(t *testing.T) {
	// version → LocalRoute: pool must not be queried at all.
	mustNotCall := newMockHost("must-not-call")
	pool := newMockPool(mustNotCall)
	cfg := map[string]config.Policy{"version": config.LocalRoute}
	h := NewOperationSelectorRouteHandler(pool, cfg, newTestCtx())

	req, fc := newTestRequest(ascii.VersionCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, string(ascii.VersionBuf.B))
	if mustNotCall.calls() != 0 {
		t.Fatalf("LocalRoute should not consult the pool; got %d calls", mustNotCall.calls())
	}
}

func TestOperationSelector_DispatchesDefaultRoute(t *testing.T) {
	// delete → DefaultRoute: only the first host is consulted.
	h0 := newMockHost("h0").program(okResp("DELETED\r\n"))
	h1 := newMockHost("h1") // would panic if called
	pool := newMockPool(h0, h1)
	cfg := map[string]config.Policy{"delete": config.DefaultRoute}
	h := NewOperationSelectorRouteHandler(pool, cfg, newTestCtx())

	req, fc := newTestRequest(ascii.DeleteCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "DELETED\r\n")
	if h0.calls() != 1 || h1.calls() != 0 {
		t.Fatalf("DefaultRoute should call only the first host: h0=%d h1=%d", h0.calls(), h1.calls())
	}
}

func TestOperationSelector_DispatchesAllFastestRoute(t *testing.T) {
	// set → AllFastestRoute: all hosts are fanned out.
	h0 := newMockHost("h0").program(okResp("STORED\r\n"))
	h1 := newMockHost("h1").program(okResp("STORED\r\n"))
	pool := newMockPool(h0, h1)
	cfg := map[string]config.Policy{"set": config.AllFastestRoute}
	h := NewOperationSelectorRouteHandler(pool, cfg, newTestCtx())

	req, fc := newTestRequest(ascii.SetCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "STORED\r\n")
	if h0.calls() != 1 || h1.calls() != 1 {
		t.Fatalf("AllFastestRoute should fan out to all hosts: h0=%d h1=%d", h0.calls(), h1.calls())
	}
}

func TestOperationSelector_UnconfiguredCommand_FallsBackToDefaultRoute(t *testing.T) {
	// "incr" is not in the policy map → handler falls back to DefaultRoute,
	// which queries only the first host.
	h0 := newMockHost("h0").program(okResp("42\r\n"))
	h1 := newMockHost("h1") // would panic if called
	pool := newMockPool(h0, h1)
	cfg := map[string]config.Policy{} // empty
	h := NewOperationSelectorRouteHandler(pool, cfg, newTestCtx())

	req, fc := newTestRequest(ascii.IncrCmd)
	h.Handle(req)

	assertSingleWrite(t, fc, "42\r\n")
	if h0.calls() != 1 || h1.calls() != 0 {
		t.Fatalf("unconfigured command should hit DefaultRoute: h0=%d h1=%d", h0.calls(), h1.calls())
	}
}

func TestNewByPolicy_ReturnsExpectedHandlerType(t *testing.T) {
	pool := newMockPool()
	ctx := newTestCtx()

	cases := []struct {
		name   string
		policy config.Policy
		want   any
	}{
		{"AllFastest", config.AllFastestRoute, &AllFastestRouteHandler{}},
		{"MissFailover", config.MissFailoverRoute, &MissFailoverRouteHandler{}},
		{"Local", config.LocalRoute, &LocalRouteHandler{}},
		{"Default", config.DefaultRoute, &DefRouteHandler{}},
		{"UnknownFallsBackToDefault", config.Policy("Bogus"), &DefRouteHandler{}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NewByPolicy(c.policy, pool, ctx)
			gotType := typeName(got)
			wantType := typeName(c.want)
			if gotType != wantType {
				t.Fatalf("policy %q: got handler %s, want %s", c.policy, gotType, wantType)
			}
		})
	}
}

func TestOperationSelector_DifferentCommandsRouteIndependently(t *testing.T) {
	// Configure set→AllFastestRoute and version→LocalRoute. A SetCmd request
	// must fan out, a VersionCmd request must hit neither host.
	h0 := newMockHost("h0").program(okResp("STORED\r\n"))
	h1 := newMockHost("h1").program(okResp("STORED\r\n"))
	pool := newMockPool(h0, h1)
	cfg := map[string]config.Policy{
		"set":     config.AllFastestRoute,
		"version": config.LocalRoute,
	}
	h := NewOperationSelectorRouteHandler(pool, cfg, newTestCtx())

	setReq, setConn := newTestRequest(ascii.SetCmd)
	h.Handle(setReq)
	assertSingleWrite(t, setConn, "STORED\r\n")
	if h0.calls() != 1 || h1.calls() != 1 {
		t.Fatalf("set should fan out: h0=%d h1=%d", h0.calls(), h1.calls())
	}

	versionReq, versionConn := newTestRequest(ascii.VersionCmd)
	h.Handle(versionReq)
	assertSingleWrite(t, versionConn, string(ascii.VersionBuf.B))
	if h0.calls() != 1 || h1.calls() != 1 {
		t.Fatalf("version must not consult hosts: h0=%d h1=%d", h0.calls(), h1.calls())
	}
}
