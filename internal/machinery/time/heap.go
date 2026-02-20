package time

type cell[T any] struct {
	ts int64
	v  T
}

type Heap[T any] struct {
	a  []cell[T]
	cb func(v T)
}

func NewHeap[T any](cap int, cb func(v T)) *Heap[T] {
	return &Heap[T]{
		a:  make([]cell[T], 0, cap),
		cb: cb,
	}
}

func (h *Heap[T]) Len() int { return len(h.a) }

func (h *Heap[T]) PeekTS() (int64, bool) {
	if len(h.a) == 0 {
		return 0, false
	}
	return h.a[0].ts, true
}

func (h *Heap[T]) Push(ts int64, v T) {
	h.a = append(h.a, cell[T]{ts: ts, v: v})
	h.siftUp(len(h.a) - 1)
}

func (h *Heap[T]) PopExpired(now int64) {
	for len(h.a) > 0 {
		if h.a[0].ts > now {
			return
		}
		c := h.pop()
		h.cb(c.v)
	}
}

func (h *Heap[T]) pop() cell[T] {
	n := len(h.a)
	out := h.a[0]
	last := h.a[n-1]
	h.a = h.a[:n-1]
	if n-1 > 0 {
		h.a[0] = last
		h.siftDown(0)
	}
	return out
}

func (h *Heap[T]) siftUp(i int) {
	for i > 0 {
		p := (i - 1) >> 1
		if h.a[p].ts <= h.a[i].ts {
			return
		}
		h.a[p], h.a[i] = h.a[i], h.a[p]
		i = p
	}
}

func (h *Heap[T]) siftDown(i int) {
	n := len(h.a)
	for {
		l := i*2 + 1
		if l >= n {
			return
		}
		r := l + 1
		s := l
		if r < n && h.a[r].ts < h.a[l].ts {
			s = r
		}
		if h.a[i].ts <= h.a[s].ts {
			return
		}
		h.a[i], h.a[s] = h.a[s], h.a[i]
		i = s
	}
}
