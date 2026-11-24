package ring

import (
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// yield lets other goroutines run while spinning on a full/empty ring.
func yield() { runtime.Gosched() }

func TestNewMPSC_InvalidSizePanics(t *testing.T) {
	for _, size := range []int{0, 1, -1, -4, 3, 6, 100} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			assert.Panics(t, func() { NewMPSC[int](size) }, "size %d", size)
		})
	}
}

func TestNewSPSC_InvalidSizePanics(t *testing.T) {
	for _, size := range []int{0, -1, -4, 3, 6, 100} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			assert.Panics(t, func() { NewSPSC[int](size) }, "size %d", size)
		})
	}
}

func TestMPSC_EmptyRing(t *testing.T) {
	r := NewMPSC[int](4)

	v, ok := r.Pop()
	assert.False(t, ok)
	assert.Zero(t, v)

	v, ok = r.Peek()
	assert.False(t, ok)
	assert.Zero(t, v)

	assert.False(t, r.Discard())
}

func TestMPSC_FIFOAndFull(t *testing.T) {
	const size = 4
	r := NewMPSC[int](size)

	for i := 0; i < size; i++ {
		require.True(t, r.Push(i), "push %d", i)
	}
	assert.False(t, r.Push(100), "push into full ring must fail")

	for i := 0; i < size; i++ {
		v, ok := r.Pop()
		require.True(t, ok)
		assert.Equal(t, i, v)
	}

	_, ok := r.Pop()
	assert.False(t, ok)
}

// TestMPSC_SmallestRingDoesNotOverwrite guards against the size-1 defect:
// with a single cell, a second Push used to succeed and overwrite the
// unconsumed value. Size 2 is the smallest accepted ring.
func TestMPSC_SmallestRingDoesNotOverwrite(t *testing.T) {
	assert.Panics(t, func() { NewMPSC[string](1) })

	r := NewMPSC[string](2)
	require.True(t, r.Push("a"))
	require.True(t, r.Push("b"))
	assert.False(t, r.Push("c"), "full ring must reject push")

	v, ok := r.Pop()
	require.True(t, ok)
	assert.Equal(t, "a", v)

	require.True(t, r.Push("d"))
	assert.False(t, r.Push("e"))

	for _, want := range []string{"b", "d"} {
		v, ok = r.Pop()
		require.True(t, ok)
		assert.Equal(t, want, v)
	}
}

func TestMPSC_WrapAround(t *testing.T) {
	// Push/pop many more items than the ring capacity to exercise
	// sequence numbers across several laps.
	r := NewMPSC[int](4)

	pushed := 0
	popped := 0
	for lap := 0; lap < 50; lap++ {
		for r.Push(pushed) {
			pushed++
		}
		// drain half, so head and tail drift relative to the slot boundaries
		for i := 0; i < 2; i++ {
			v, ok := r.Pop()
			require.True(t, ok)
			require.Equal(t, popped, v)
			popped++
		}
	}
	for {
		v, ok := r.Pop()
		if !ok {
			break
		}
		require.Equal(t, popped, v)
		popped++
	}
	assert.Equal(t, pushed, popped, "every pushed value must be popped")
}

func TestMPSC_PeekAndDiscard(t *testing.T) {
	r := NewMPSC[*int](2)
	a, b := 1, 2

	require.True(t, r.Push(&a))
	require.True(t, r.Push(&b))

	// Peek does not consume.
	v, ok := r.Peek()
	require.True(t, ok)
	assert.Same(t, &a, v)
	v, ok = r.Peek()
	require.True(t, ok)
	assert.Same(t, &a, v)

	// Discard consumes and frees the slot for a new push.
	require.True(t, r.Discard())
	assert.Nil(t, r.buf[0].val, "discarded slot must not retain the value")

	v, ok = r.Peek()
	require.True(t, ok)
	assert.Same(t, &b, v)

	c := 3
	require.True(t, r.Push(&c))
	assert.False(t, r.Push(&c))

	v, ok = r.Pop()
	require.True(t, ok)
	assert.Same(t, &b, v)
	assert.Nil(t, r.buf[1].val, "popped slot must not retain the value")

	v, ok = r.Pop()
	require.True(t, ok)
	assert.Same(t, &c, v)

	assert.False(t, r.Discard())
}

// TestMPSC_ConcurrentProducers checks that, with many producers racing on
// Push and a single consumer, every pushed value is delivered exactly once
// and each producer's values arrive in the order they were pushed.
func TestMPSC_ConcurrentProducers(t *testing.T) {
	const (
		producers   = 8
		perProducer = 5000
	)
	type item struct {
		producer, seq int
	}

	r := NewMPSC[item](64)

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				for !r.Push(item{producer: p, seq: i}) {
					// ring full: let the consumer catch up
					yield()
				}
			}
		}(p)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	lastSeq := make([]int, producers)
	for i := range lastSeq {
		lastSeq[i] = -1
	}
	received := 0
	producersDone := false
	for received < producers*perProducer {
		v, ok := r.Pop()
		if !ok {
			if producersDone {
				// all producers finished and the ring is empty: items were lost
				break
			}
			select {
			case <-done:
				producersDone = true
			default:
				yield()
			}
			continue
		}
		require.Equal(t, lastSeq[v.producer]+1, v.seq, "producer %d out of order", v.producer)
		lastSeq[v.producer] = v.seq
		received++
	}

	assert.Equal(t, producers*perProducer, received)
	for p, s := range lastSeq {
		assert.Equal(t, perProducer-1, s, "producer %d", p)
	}
	_, ok := r.Pop()
	assert.False(t, ok, "ring must be empty")
}

func TestSPSC_PushPop(t *testing.T) {
	r := NewSPSC[int](4)

	v, ok := r.Pop()
	assert.False(t, ok)
	assert.Zero(t, v)

	for i := 0; i < 4; i++ {
		require.True(t, r.CanPush())
		r.Push(i)
	}
	assert.False(t, r.CanPush())

	for i := 0; i < 4; i++ {
		v, ok = r.Pop()
		require.True(t, ok)
		assert.Equal(t, i, v)
		assert.True(t, r.CanPush())
	}

	_, ok = r.Pop()
	assert.False(t, ok)
}

func TestSPSC_WrapAroundAndClearsSlots(t *testing.T) {
	r := NewSPSC[*int](2)

	vals := make([]int, 100)
	for i := range vals {
		vals[i] = i
		require.True(t, r.CanPush())
		r.Push(&vals[i])

		v, ok := r.Pop()
		require.True(t, ok)
		assert.Same(t, &vals[i], v)
	}
	for i, slot := range r.buf {
		assert.Nil(t, slot, "slot %d must not retain popped value", i)
	}
}

func TestSPSC_SizeOne(t *testing.T) {
	r := NewSPSC[int](1)
	require.True(t, r.CanPush())
	r.Push(7)
	assert.False(t, r.CanPush())
	v, ok := r.Pop()
	require.True(t, ok)
	assert.Equal(t, 7, v)
	assert.True(t, r.CanPush())
}

// TestSPSC_ProducerConsumerHandoff uses the SPSC with one producer and one
// consumer goroutine, synchronised through a mutex as its owners must do
// (SPSC is documented as not concurrency-safe).
func TestSPSC_ProducerConsumerHandoff(t *testing.T) {
	const n = 10000
	r := NewSPSC[int](16)
	var mu sync.Mutex

	go func() {
		for i := 0; i < n; {
			mu.Lock()
			if r.CanPush() {
				r.Push(i)
				i++
			}
			mu.Unlock()
		}
	}()

	for expected := 0; expected < n; {
		mu.Lock()
		v, ok := r.Pop()
		mu.Unlock()
		if !ok {
			yield()
			continue
		}
		require.Equal(t, expected, v)
		expected++
	}
}

func BenchmarkMPSC_PushPop(b *testing.B) {
	r := NewMPSC[int](1024)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Push(i)
		r.Pop()
	}
}

func BenchmarkMPSC_ParallelPush(b *testing.B) {
	r := NewMPSC[int](1 << 16)
	var mu sync.Mutex
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if !r.Push(1) {
				// single consumer: serialise draining
				mu.Lock()
				for {
					if _, ok := r.Pop(); !ok {
						break
					}
				}
				mu.Unlock()
			}
		}
	})
}

func BenchmarkSPSC_PushPop(b *testing.B) {
	r := NewSPSC[int](1024)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Push(i)
		r.Pop()
	}
}
