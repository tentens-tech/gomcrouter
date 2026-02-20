package time

import (
	"github.com/stretchr/testify/assert"
	"testing"
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
