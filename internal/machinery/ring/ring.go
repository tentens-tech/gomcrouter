package ring

import (
	"runtime"
	"sync/atomic"
)

type mpscCell[T any] struct {
	seq atomic.Uint64
	val T
}

// MPSC stays for multi-producer single-consumer ring buffer.
// Concurrency-safe for push operations only.
type MPSC[T any] struct {
	mask uint64
	buf  []mpscCell[T]

	tail atomic.Uint64
	head uint64
}

func NewMPSC[T any](size int) *MPSC[T] {
	if size&(size-1) != 0 {
		panic("ring size must be a power of 2")
	}

	ring := &MPSC[T]{
		mask: uint64(size - 1),
		buf:  make([]mpscCell[T], size),
	}
	for i := 0; i < size; i++ {
		ring.buf[i].seq.Store(uint64(i))
	}
	ring.tail.Store(0)
	ring.head = 0
	return ring
}

func (r *MPSC[T]) Push(v T) bool {
	for {
		pos := r.tail.Load()
		cell := &r.buf[pos&r.mask]
		seq := cell.seq.Load()
		dif := int64(seq) - int64(pos)

		if dif == 0 {
			if r.tail.CompareAndSwap(pos, pos+1) {
				cell.val = v
				cell.seq.Store(pos + 1)
				return true
			}
			// lost the race, retry
			continue
		}

		if dif < 0 {
			// seq < pos => consumer hasn't advanced enough => ring is full
			return false
		}

		// another producer is working on this slot/position; backoff a bit.
		runtime.Gosched()
	}
}

func (r *MPSC[T]) Pop() (T, bool) {
	pos := r.head
	cell := &r.buf[pos&r.mask]

	seq := cell.seq.Load()
	if seq != pos+1 {
		var zero T
		return zero, false
	}

	v := cell.val

	var zero T
	cell.val = zero

	ringSize := r.mask + 1
	cell.seq.Store(pos + ringSize)

	r.head = pos + 1
	return v, true
}

func (r *MPSC[T]) Peek() (T, bool) {
	pos := r.head
	cell := &r.buf[pos&r.mask]

	seq := cell.seq.Load()
	if seq != pos+1 {
		var zero T
		return zero, false
	}

	return cell.val, true
}

func (r *MPSC[T]) Discard() bool {
	pos := r.head
	cell := &r.buf[pos&r.mask]

	seq := cell.seq.Load()
	if seq != pos+1 {
		return false
	}

	var zero T
	cell.val = zero

	ringSize := r.mask + 1
	cell.seq.Store(pos + ringSize)

	r.head = pos + 1
	return true
}

// SPSC stays for single-producer single-consumer ring buffer.
// Not concurrency-safe.
type SPSC[T any] struct {
	mask uint64
	buf  []T

	head uint64
	tail uint64
}

func NewSPSC[T any](size int) *SPSC[T] {
	if size <= 0 || size&(size-1) != 0 {
		panic("ring size must be a power of 2")
	}
	return &SPSC[T]{
		mask: uint64(size - 1),
		buf:  make([]T, size),
	}
}

func (r *SPSC[T]) CanPush() bool {
	return (r.tail - r.head) != (r.mask + 1)
}

func (r *SPSC[T]) Push(v T) {
	r.buf[r.tail&r.mask] = v
	r.tail++
}

func (r *SPSC[T]) Pop() (T, bool) {
	if r.head == r.tail {
		var zero T
		return zero, false
	}
	i := r.head & r.mask
	v := r.buf[i]

	var zero T
	r.buf[i] = zero

	r.head++
	return v, true
}
