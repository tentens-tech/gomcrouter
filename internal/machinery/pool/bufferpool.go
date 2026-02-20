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

	c := cap(bb.B)
	bb.B = bb.B[:c]

	switch c {
	case XSmallBufferSize:
		p.xs.Put(bb)
	case SmallBufferSize:
		p.s.Put(bb)
	case MediumBufferSize:
		p.m.Put(bb)
	case LargeBufferSize:
		p.l.Put(bb)
	case XLargeBufferSize:
		p.xl.Put(bb)
	case XXLargeBufferSize:
		p.xxl.Put(bb)
	}
}
