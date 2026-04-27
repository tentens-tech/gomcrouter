package upstream

import (
	"sync"
	"testing"
	"time"

	"github.com/tentens-tech/gomcrouter/internal/config"
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
	logger, _ := zap.NewProduction()
	p := &OrderedPool{
		ctx: &config.AppContext{
			Logger: logger.Sugar(),
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
