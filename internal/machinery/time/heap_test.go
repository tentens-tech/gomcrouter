package time

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHeap_OrderAndPopExpired(t *testing.T) {
	got := make([]int, 0, 8)
	h := NewHeap[int](64, func(v int) { got = append(got, v) })

	h.Push(10, 1)
	h.Push(20, 2)
	h.Push(20, 22)
	h.Push(30, 3)

	ts, ok := h.PeekTS()
	assert.True(t, ok)
	assert.Equal(t, int64(10), ts)

	h.PopExpired(9)
	assert.Empty(t, got)

	h.PopExpired(10)
	assert.Equal(t, []int{1}, got)

	h.PopExpired(19)
	assert.Equal(t, []int{1}, got)

	h.PopExpired(20)
	assert.Len(t, got, 3)
	assert.Equal(t, 1, got[0])
	assert.ElementsMatch(t, []int{2, 22}, got[1:])

	ts, ok = h.PeekTS()
	assert.True(t, ok)
	assert.Equal(t, int64(30), ts)

	h.PopExpired(30)
	assert.Len(t, got, 4)
	assert.Equal(t, 3, got[3])

	_, ok = h.PeekTS()
	assert.False(t, ok)
	assert.Equal(t, 0, h.Len())
}

func TestHeap_EmptyPopExpiredIsNoop(t *testing.T) {
	calls := 0
	h := NewHeap[int](0, func(int) { calls++ })

	h.PopExpired(1 << 62)
	assert.Zero(t, calls)
	assert.Equal(t, 0, h.Len())
}

// TestHeap_RandomOrder pushes timestamps in pseudo-random order (with
// duplicates) and checks they are expired in non-decreasing order, which
// exercises both siftUp and siftDown along every branch.
func TestHeap_RandomOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2)) // deterministic seed
	const n = 1000

	var got []int64
	h := NewHeap[int64](4, func(v int64) { got = append(got, v) })

	want := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		ts := rng.Int64N(200)
		want = append(want, ts)
		h.Push(ts, ts)
	}
	assert.Equal(t, n, h.Len())

	minTS, ok := h.PeekTS()
	assert.True(t, ok)
	assert.Equal(t, slices.Min(want), minTS)

	// expire in several steps, interleaving new pushes in the future
	for now := int64(0); now < 200; now += 37 {
		h.PopExpired(now)
		h.Push(1000+now, 1000+now)
	}
	h.PopExpired(1 << 62)

	slices.Sort(want)
	for now := int64(0); now < 200; now += 37 {
		want = append(want, 1000+now)
	}
	assert.Equal(t, want, got)
	assert.True(t, slices.IsSorted(got))
	assert.Equal(t, 0, h.Len())
}

func BenchmarkHeap_PushPopExpired(b *testing.B) {
	h := NewHeap[int](1024, func(int) {})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.Push(int64(i), i)
		if h.Len() >= 1024 {
			h.PopExpired(int64(i))
		}
	}
}
