package pool

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

func TestBufferPool_GetSizeClasses(t *testing.T) {
	tests := []struct {
		size    int
		wantCap int
	}{
		{size: 0, wantCap: XSmallBufferSize},
		{size: 1, wantCap: XSmallBufferSize},
		{size: XSmallBufferSize, wantCap: XSmallBufferSize},
		{size: XSmallBufferSize + 1, wantCap: SmallBufferSize},
		{size: SmallBufferSize, wantCap: SmallBufferSize},
		{size: SmallBufferSize + 1, wantCap: MediumBufferSize},
		{size: MediumBufferSize, wantCap: MediumBufferSize},
		{size: MediumBufferSize + 1, wantCap: LargeBufferSize},
		{size: LargeBufferSize, wantCap: LargeBufferSize},
		{size: LargeBufferSize + 1, wantCap: XLargeBufferSize},
		{size: XLargeBufferSize, wantCap: XLargeBufferSize},
		{size: XLargeBufferSize + 1, wantCap: XXLargeBufferSize},
		{size: XXLargeBufferSize, wantCap: XXLargeBufferSize},
		// oversize buffers are allocated exactly, not pooled
		{size: XXLargeBufferSize + 1, wantCap: XXLargeBufferSize + 1},
		{size: 1 << 20, wantCap: 1 << 20},
	}

	p := newBufferPool()
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.size), func(t *testing.T) {
			bb := p.Get(tt.size)
			require.NotNil(t, bb)
			assert.Len(t, bb.B, tt.size)
			assert.Equal(t, tt.wantCap, cap(bb.B))
			p.Put(bb)
		})
	}
}

func TestBufferPool_PutRestoresFullLength(t *testing.T) {
	p := newBufferPool()
	for _, size := range []int{XSmallBufferSize, SmallBufferSize, MediumBufferSize, LargeBufferSize, XLargeBufferSize, XXLargeBufferSize} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			bb := p.Get(size)
			bb.B = bb.B[:10]
			p.Put(bb)
			assert.Len(t, bb.B, size, "pooled buffer must be resliced to its full capacity")
		})
	}
}

func TestBufferPool_PutIgnoresForeignBuffers(t *testing.T) {
	p := newBufferPool()

	// nil must be a no-op
	assert.NotPanics(t, func() { p.Put(nil) })

	// Static responses (e.g. ascii.*Buf) have arbitrary capacities and must
	// never be resliced: that would leak trailing bytes on the next write.
	backing := []byte("SERVER_ERROR something\r\ntrailing-garbage")
	static := &types.ByteBuf{B: backing[:24]}
	p.Put(static)
	assert.Equal(t, "SERVER_ERROR something\r\n", string(static.B))

	// A non-pool buffer whose len < cap but cap is not a size class is left alone.
	odd := &types.ByteBuf{B: make([]byte, 5, XSmallBufferSize+3)}
	p.Put(odd)
	assert.Len(t, odd.B, 5)
}

func TestBufferPool_GlobalInstance(t *testing.T) {
	require.NotNil(t, BufferPool)
	bb := BufferPool.Get(100)
	assert.Len(t, bb.B, 100)
	BufferPool.Put(bb)
}

func TestBufferPool_Concurrent(t *testing.T) {
	p := newBufferPool()
	sizes := []int{1, XSmallBufferSize + 1, SmallBufferSize + 1, MediumBufferSize + 1, LargeBufferSize + 1, XLargeBufferSize + 1, XXLargeBufferSize + 1}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				size := sizes[(g+i)%len(sizes)]
				bb := p.Get(size)
				if len(bb.B) != size {
					t.Errorf("got len %d, want %d", len(bb.B), size)
					return
				}
				// Write the whole buffer: under -race this detects a buffer
				// handed out to two goroutines at once.
				marker := byte(g)
				for j := range bb.B {
					bb.B[j] = marker
				}
				for j := range bb.B {
					if bb.B[j] != marker {
						t.Errorf("buffer shared between goroutines")
						return
					}
				}
				p.Put(bb)
			}
		}(g)
	}
	wg.Wait()
}

func TestRequestPools(t *testing.T) {
	req, ok := RequestPool.Get().(*types.Request)
	require.True(t, ok)
	require.NotNil(t, req)
	RequestPool.Put(req)

	in, ok := InflightRequestPool.Get().(*types.InflightRequest)
	require.True(t, ok)
	require.NotNil(t, in)
	InflightRequestPool.Put(in)
}

func BenchmarkBufferPool_GetPut(b *testing.B) {
	for _, size := range []int{64, SmallBufferSize, XLargeBufferSize} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				BufferPool.Put(BufferPool.Get(size))
			}
		})
	}
}

func BenchmarkBufferPool_GetPutParallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			BufferPool.Put(BufferPool.Get(SmallBufferSize))
		}
	})
}
