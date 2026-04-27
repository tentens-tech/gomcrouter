package pool

import (
	"github.com/tentens-tech/gomcrouter/internal/types"
	"sync"
)

const (
	XSmallBufferSize  = 1024
	SmallBufferSize   = 4096
	MediumBufferSize  = 8192
	LargeBufferSize   = 16384
	XLargeBufferSize  = 131072
	XXLargeBufferSize = 262144
)

type bufferPool struct {
	xs, s, m, l, xl, xxl sync.Pool
}

var BufferPool = newBufferPool()

func newBufferPool() *bufferPool {
	p := &bufferPool{}

	p.xs.New = func() any {
		return &types.ByteBuf{
			B: make([]byte, XSmallBufferSize),
		}
	}
	p.s.New = func() any {
		return &types.ByteBuf{
			B: make([]byte, SmallBufferSize),
		}
	}
	p.m.New = func() any {
		return &types.ByteBuf{
			B: make([]byte, MediumBufferSize),
		}
	}
	p.l.New = func() any {
		return &types.ByteBuf{
			B: make([]byte, LargeBufferSize),
		}
	}
	p.xl.New = func() any {
		return &types.ByteBuf{
			B: make([]byte, XLargeBufferSize),
		}
	}
	p.xxl.New = func() any {
		return &types.ByteBuf{
			B: make([]byte, XXLargeBufferSize),
		}
	}

	return p
}

func (p *bufferPool) Get(size int) *types.ByteBuf {
	switch {
	case size <= XSmallBufferSize:
		bb := p.xs.Get().(*types.ByteBuf)
		bb.B = bb.B[:size]
		return bb
	case size <= SmallBufferSize:
		bb := p.s.Get().(*types.ByteBuf)
		bb.B = bb.B[:size]
		return bb
	case size <= MediumBufferSize:
		bb := p.m.Get().(*types.ByteBuf)
		bb.B = bb.B[:size]
		return bb
	case size <= LargeBufferSize:
		bb := p.l.Get().(*types.ByteBuf)
		bb.B = bb.B[:size]
		return bb
	case size <= XLargeBufferSize:
		bb := p.xl.Get().(*types.ByteBuf)
		bb.B = bb.B[:size]
		return bb
	case size <= XXLargeBufferSize:
		bb := p.xxl.Get().(*types.ByteBuf)
		bb.B = bb.B[:size]
		return bb
	default:
		// oversize — not pooled
		return &types.ByteBuf{B: make([]byte, size)}
	}
}

func (p *bufferPool) Put(bb *types.ByteBuf) {
	if bb == nil {
		return
	}

	// Only buffers whose capacity matches a pooled size class get returned
	// to a pool. Reslicing to cap is required before pooling (since Get may
	// have shortened the slice via bb.B[:size]) but must NOT happen for
	// non-pool buffers — including the static error responses in ascii/* —
	// because that would mutate their slice header and surface trailing
	// uninitialized bytes on subsequent writes.
	switch cap(bb.B) {
	case XSmallBufferSize:
		bb.B = bb.B[:XSmallBufferSize]
		p.xs.Put(bb)
	case SmallBufferSize:
		bb.B = bb.B[:SmallBufferSize]
		p.s.Put(bb)
	case MediumBufferSize:
		bb.B = bb.B[:MediumBufferSize]
		p.m.Put(bb)
	case LargeBufferSize:
		bb.B = bb.B[:LargeBufferSize]
		p.l.Put(bb)
	case XLargeBufferSize:
		bb.B = bb.B[:XLargeBufferSize]
		p.xl.Put(bb)
	case XXLargeBufferSize:
		bb.B = bb.B[:XXLargeBufferSize]
		p.xxl.Put(bb)
	}
}
