package upstream

import (
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/upstream/internal/upstreamtest"
	"go.uber.org/zap"
)

type fakeHost struct {
	id   int
	addr string

	onHealthy   func(int)
	onUnhealthy func(int)
}

func newFakeHost(id int, addr string, onH, onU func(int)) *fakeHost {
	return &fakeHost{
		id:          id,
		addr:        addr,
		onHealthy:   onH,
		onUnhealthy: onU,
	}
}

func (h *fakeHost) GoHealthy() {
	h.onHealthy(h.id)
}

func (h *fakeHost) GoUnhealthy() {
	h.onUnhealthy(h.id)
}

func mkPoolWithState(healthyIDs []int, unhealthyIDs []int) (*OrderedPool, map[int]*fakeHost) {
	p := &OrderedPool{
		ctx: &config.AppContext{
			Logger: zap.NewNop().Sugar(),
		},
	}

	hosts := make(map[int]*fakeHost)

	mk := func(id int) *fakeHost { //nolint
		h := newFakeHost(id, "h", p.onHostHealthy, p.onHostUnhealthy)
		hosts[id] = h
		return h
	}

	for _, id := range healthyIDs {
		_ = mk(id)
		p.healthyHosts = append(p.healthyHosts, &OrderedHost{Host: nil, num: id, hostname: "h"})
	}

	for _, id := range unhealthyIDs {
		_ = mk(id)
		p.unhealthyHosts = append(p.unhealthyHosts, &OrderedHost{Host: nil, num: id, hostname: "h"})
	}

	return p, hosts
}

func nums(hs []*OrderedHost) []int {
	out := make([]int, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.num)
	}
	return out
}

func assertNums(t *testing.T, got []*OrderedHost, want []int) {
	t.Helper()
	gn := nums(got)
	if len(gn) != len(want) {
		t.Fatalf("got %v want %v", gn, want)
	}
	for i := range want {
		if gn[i] != want[i] {
			t.Fatalf("got %v want %v", gn, want)
		}
	}
}

func TestOrderedPool_FakeHosts_OrderOnRecovery(t *testing.T) {
	p, hosts := mkPoolWithState(
		[]int{0, 2, 5},
		[]int{1, 4},
	)

	hosts[4].GoHealthy()
	assertNums(t, p.healthyHosts, []int{0, 2, 4, 5})
	assertNums(t, p.unhealthyHosts, []int{1})

	hosts[1].GoHealthy()
	assertNums(t, p.healthyHosts, []int{0, 1, 2, 4, 5})
	assertNums(t, p.unhealthyHosts, []int{})
}

func TestOrderedPool_FakeHosts_OrderOnFailure(t *testing.T) {
	p, hosts := mkPoolWithState(
		[]int{0, 1, 2, 4, 5},
		[]int{},
	)

	hosts[2].GoUnhealthy()
	assertNums(t, p.healthyHosts, []int{0, 1, 4, 5})

	hosts[0].GoUnhealthy()
	assertNums(t, p.healthyHosts, []int{1, 4, 5})
}

// TestOrderedPool_All_ConcurrentWithTransitions exercises the publication
// of healthyHostsView. With a plain slice field, the writer's slice-header
// assignment racing against the reader's slice-header load would trip the
// race detector. atomic.Pointer makes the publication race-free.
func TestOrderedPool_All_ConcurrentWithTransitions(t *testing.T) {
	p, hosts := mkPoolWithState([]int{0, 1, 2}, []int{})

	var wg sync.WaitGroup
	deadline := time.Now().Add(50 * time.Millisecond)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			hosts[0].GoUnhealthy()
			hosts[0].GoHealthy()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			_ = p.All()
		}
	}()

	wg.Wait()
}

func TestOrderedPool_FakeHosts_OrderFull(t *testing.T) {
	p, hosts := mkPoolWithState(
		[]int{0, 1, 2, 4, 5},
		[]int{},
	)

	for _, host := range hosts {
		host.GoUnhealthy()
	}

	assertNums(t, p.healthyHosts, []int{})

	for _, host := range hosts {
		host.GoHealthy()
	}

	assertNums(t, p.healthyHosts, []int{0, 1, 2, 4, 5})
	assertNums(t, p.unhealthyHosts, []int{})
}

func viewNums(t *testing.T, view []AsyncDoer) []int {
	t.Helper()
	out := make([]int, 0, len(view))
	for _, d := range view {
		oh, ok := d.(*OrderedHost)
		require.True(t, ok)
		out = append(out, oh.num)
	}
	return out
}

func healthyHostsGauge(t *testing.T) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == "gomcrouter_upstream_healthy_hosts_total" {
			require.Len(t, mf.GetMetric(), 1)
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	require.FailNow(t, "healthy hosts gauge not found")
	return 0
}

func TestOrderedPool_WithRealHosts(t *testing.T) {
	srv0 := upstreamtest.NewServer(t)
	srv1 := upstreamtest.NewServer(t)
	srv2 := upstreamtest.NewServer(t)
	srv1.Stop() // down at start-up

	e := newEnv(t)
	p := NewOrderedPool(e.ctx, config.OrderedPoolConfig{srv0.Addr(), srv1.Addr(), srv2.Addr()}, e.eng)
	hosts := map[int]*Host{}
	for _, oh := range append(append([]*OrderedHost(nil), p.healthyHosts...), p.unhealthyHosts...) {
		hosts[oh.num] = oh.Host
		stopHost(t, e.cancel, oh.Host)
	}
	require.Len(t, hosts, 3)

	assert.Equal(t, []int{0, 2}, viewNums(t, p.All()))
	assertNums(t, p.unhealthyHosts, []int{1})
	p.scrapeNumHealthyHostsMetric()
	assert.InDelta(t, 2, healthyHostsGauge(t), 0)

	// host 1 comes up: it is inserted in config order
	srv1.Restart()
	hosts[1].healthCheck()
	assert.Equal(t, []int{0, 1, 2}, viewNums(t, p.All()))
	assert.Empty(t, p.unhealthyHosts)

	// host 0 goes down
	srv0.Stop()
	hosts[0].healthCheck()
	assert.Equal(t, []int{1, 2}, viewNums(t, p.All()))
	p.scrapeNumHealthyHostsMetric()
	assert.InDelta(t, 2, healthyHostsGauge(t), 0)

	// everything down
	srv1.Stop()
	srv2.Stop()
	hosts[1].healthCheck()
	hosts[2].healthCheck()
	assert.Nil(t, p.All())
	p.scrapeNumHealthyHostsMetric()
	assert.InDelta(t, 0, healthyHostsGauge(t), 0)

	// the view handed out before a transition is a stable snapshot
	srv2.Restart()
	before := p.All()
	hosts[2].healthCheck()
	assert.Nil(t, before)
	assert.Equal(t, []int{2}, viewNums(t, p.All()))
}

func TestOrderedPool_Empty(t *testing.T) {
	e := newEnv(t)
	p := NewOrderedPool(e.ctx, config.OrderedPoolConfig{}, e.eng)
	assert.Nil(t, p.All())
}

func TestOrderedPool_UnknownOrDuplicateTransitions(t *testing.T) {
	p, hosts := mkPoolWithState([]int{0, 2}, []int{1})

	// already healthy / already unhealthy / unknown ids are ignored
	hosts[0].GoHealthy()
	hosts[1].GoUnhealthy()
	p.onHostHealthy(42)
	p.onHostUnhealthy(42)

	assertNums(t, p.healthyHosts, []int{0, 2})
	assertNums(t, p.unhealthyHosts, []int{1})
}
