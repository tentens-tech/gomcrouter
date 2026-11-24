package registry

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostRegistry_RegisterAndGet(t *testing.T) {
	r := NewHostRegistry()

	assert.Equal(t, 0, r.RegisterHost("a:11211"))
	assert.Equal(t, 1, r.RegisterHost("b:11211"))
	// duplicates get their own id: one id per upstream host instance
	assert.Equal(t, 2, r.RegisterHost("a:11211"))

	assert.Equal(t, "a:11211", r.GetHost(0))
	assert.Equal(t, "b:11211", r.GetHost(1))
	assert.Equal(t, "a:11211", r.GetHost(2))
}

func TestHostRegistry_GetUnknownIdPanics(t *testing.T) {
	r := NewHostRegistry()
	assert.Panics(t, func() { r.GetHost(0) })

	r.RegisterHost("a")
	assert.Panics(t, func() { r.GetHost(1) })
	assert.Panics(t, func() { r.GetHost(-1) })
}

func TestHostRegistry_Concurrent(t *testing.T) {
	r := NewHostRegistry()
	const n = 100

	ids := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := r.RegisterHost(fmt.Sprintf("host-%d", i))
			ids[i] = id
			// read back concurrently with other registrations
			if got := r.GetHost(id); got != fmt.Sprintf("host-%d", i) {
				t.Errorf("GetHost(%d) = %q", id, got)
			}
		}(i)
	}
	wg.Wait()

	seen := make(map[int]bool, n)
	for i, id := range ids {
		require.False(t, seen[id], "duplicate id %d", id)
		seen[id] = true
		assert.Equal(t, fmt.Sprintf("host-%d", i), r.GetHost(id))
	}
	assert.Len(t, seen, n)
}

func TestGlobalHostRegistry(t *testing.T) {
	require.NotNil(t, GlobalHostRegistry)
	id := GlobalHostRegistry.RegisterHost("global-test")
	assert.Equal(t, "global-test", GlobalHostRegistry.GetHost(id))
}
