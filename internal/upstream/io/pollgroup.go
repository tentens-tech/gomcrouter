package io

import (
	"fmt"
	"github.com/pkg/errors"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/machinery/ring"
	"github.com/tentens-tech/gomcrouter/internal/machinery/time"
	"github.com/tentens-tech/gomcrouter/internal/observability/metric"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream/netpoll"
	"github.com/tentens-tech/gomcrouter/internal/upstream/socket"
	"golang.org/x/sys/unix"
	"sync/atomic"
)

const (
	SocketIOBatch = 1024
)

var (
	ErrBusy    = errors.New("busy")
	ErrTimeout = errors.New("timeout")
	ErrClosed  = errors.New("pollgroup is closed")

	ErrClosedByClient = errors.New("pollgroup was closed by client")
)

type PollGroup struct {
	active atomic.Bool

	addr   string
	hostId int
	ctx    *config.AppContext

	poller *netpoll.Poller

	sockets     []*socket.Socket
	requestRing *ring.MPSC[*types.Request]
	timeoutHeap *time.Heap[*types.InflightRequest]

	onFailureHook             func()
	onPollerFailureEngineHook func(error)
}

func newPollGroup(
	addr string,
	hostId int,
	ctx *config.AppContext,
	poller *netpoll.Poller,
	onPollerFailure func(error),
	onFailureHook func(),
) (*PollGroup, error) {
	pg := &PollGroup{
		addr:                      addr,
		hostId:                    hostId,
		ctx:                       ctx,
		poller:                    poller,
		onFailureHook:             onFailureHook,
		onPollerFailureEngineHook: onPollerFailure,
		requestRing:               ring.NewMPSC[*types.Request](DefaultRingSize),
	}

	pg.timeoutHeap = time.NewHeap[*types.InflightRequest](DefaultRingSize, pg.expireInflightRequest)

	pg.active.Store(true)

	err := pg.init()
	if err != nil {
		ctx.Logger.Warnf("failed to initialize poll group for host %s: %v", addr, err)
		return nil, err
	}

	ctx.Logger.Infof("created pollgroup for host: %s", addr)

	return pg, nil
}

func (p *PollGroup) init() error {
	for i := 0; i < p.ctx.Config.UpstreamConnections; i++ {
		err := p.createSocket(i)
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *PollGroup) createSocket(idx int) error {
	newSock, err := socket.NewSocket(p.ctx, p.addr)
	if err != nil {
		return err
	}

	p.poller.Register(
		newSock,
		p.onReadable,
		p.onWriteable,
		p.onSocketErr,
	)

	if len(p.sockets) <= idx {
		p.sockets = append(p.sockets, newSock)
		return nil
	}

	p.sockets[idx] = newSock
	return nil
}

func (p *PollGroup) IsActive() bool {
	return p.active.Load()
}

func (p *PollGroup) HostId() int {
	return p.hostId
}

func (p *PollGroup) NumSockets() int {
	return len(p.sockets)
}

func (p *PollGroup) Enqueue(req *types.Request) bool {
	if !p.active.Load() {
		req.Callback(nil, ErrClosed)
		metric.Collector.HandleUpstreamError(req.Cmd, p.hostId)
		return false
	}

	if !p.requestRing.Push(req) {
		req.Callback(nil, ErrBusy)
		metric.Collector.HandleUpstreamError(req.Cmd, p.hostId)
		return false
	}

	return true
}

func (p *PollGroup) Drain() {
	p.drainPollGroup(ErrClosedByClient)
}

func (p *PollGroup) expireInflightRequest(r *types.InflightRequest) {
	// request was not seen in onReadable(). can't be safely returned to pool.
	if r.Status == types.InflightRequestStatus {
		r.Status = types.CancelledRequestStatus
		r.Request.Callback(nil, ErrTimeout)
		metric.Collector.HandleUpstreamError(r.Request.Cmd, p.hostId)
		return
	}

	// request was already seen in onReadable(). can be safely returned to pool.
	if r.Status == types.DoneRequestStatus {
		pool.InflightRequestPool.Put(r)
	}
}

func (p *PollGroup) dispatch() {
	p.timeoutHeap.PopExpired(time.NanoTime())

	for _, s := range p.sockets {
		p.dispatchOnSocket(s)

		if err := p.poller.Mod(s); err != nil {
			p.onFailureHook()
			p.onPollerFailureEngineHook(err)
			return
		}
	}
}

func (p *PollGroup) dispatchOnSocket(s *socket.Socket) {
	now := time.NanoTime()
	for i := 0; i < SocketIOBatch; i++ {
		if !s.CanPushToInflight() {
			return
		}

		// try to peek from requestRing, then try to write to the socket.
		// if succeeds, discard peeked object from request ring, and push to socket's inflight ring.
		req, ok := p.requestRing.Peek()
		if !ok {
			return
		}

		if !s.Write(req.Raw.B) {
			return
		}

		in := pool.InflightRequestPool.Get().(*types.InflightRequest)
		in.Request = req
		in.Ts = now
		in.Status = types.InflightRequestStatus

		_ = p.requestRing.Discard()
		s.PushToInflight(in)

		p.timeoutHeap.Push(now+p.ctx.Config.TimeoutNs, in)
	}
}

func (p *PollGroup) onWriteable(s *socket.Socket) {
	err := s.Flush()
	if err != nil {
		p.recreateSocket(s, err)
	}
}

func (p *PollGroup) onReadable(s *socket.Socket) {
	b, err := s.Read()
	if err != nil {
		p.recreateSocket(s, err)
		return
	}

	offset := 0

	now := time.NanoTime()
	for i := 0; i < SocketIOBatch; i++ {
		n, err := ascii.DecodeResponse(b[offset:])
		if err != nil {
			p.recreateSocket(s, fmt.Errorf("ascii decode response failed, order lost"))
			return
		}
		if n == 0 {
			break
		}

		inflightReq, ok := s.PopFromInflight()
		if !ok {
			p.recreateSocket(s, fmt.Errorf("socket %d has empty inflight, request lost", s.FD()))
			return
		}

		// return to pool only if cancelled (timeout heap does not reference inflightReq anymore).
		// otherwise, it will be returned inside PopExpired calls.
		if inflightReq.Status == types.CancelledRequestStatus {
			pool.InflightRequestPool.Put(inflightReq)
			offset += n
			continue
		}

		inflightReq.Status = types.DoneRequestStatus

		respByteBuf := pool.BufferPool.Get(n)
		copy(respByteBuf.B, b[offset:offset+n])
		offset += n

		hitStatus := ascii.GetResponseUnknown
		if inflightReq.Request.Cmd == ascii.GetCmd || inflightReq.Request.Cmd == ascii.GetsCmd {
			hitStatus = ascii.ClassifyGetResponse(respByteBuf.B)
		}

		metric.Collector.HandleUpstreamRequestAsync(
			inflightReq.Request.Cmd,
			p.hostId,
			now-inflightReq.Ts,
			hitStatus,
		)
		inflightReq.Request.Callback(respByteBuf, err)
	}

	s.Discard(offset)
}

func (p *PollGroup) onSocketErr(s *socket.Socket) {
	p.recreateSocket(s, getSocketError(s.FD()))
}

func (p *PollGroup) recreateSocket(s *socket.Socket, err error) {
	p.drainSocketWithErr(s, err)

	var sockIdx int
	var found bool
	for i := 0; i < len(p.sockets); i++ {
		if p.sockets[i].FD() == s.FD() {
			sockIdx = i
			found = true
			break
		}
	}

	if !found {
		return
	}

	// create new socket in the place of old one
	err = p.createSocket(sockIdx)
	if err != nil {
		p.onFailureHook()
	}
}

func (p *PollGroup) drainPollGroup(err error) {
	p.active.Store(false)

	for _, sock := range p.sockets {
		p.drainSocketWithErr(sock, err)
	}

	for {
		req, ok := p.requestRing.Pop()
		if !ok {
			break
		}

		req.Callback(nil, errors.Wrap(err, "poll group was closed"))
		metric.Collector.HandleUpstreamError(req.Cmd, p.hostId)
	}

	p.sockets = nil
}

func (p *PollGroup) drainSocketWithErr(s *socket.Socket, err error) {
	for {
		inflightReq, ok := s.PopFromInflight()
		if !ok {
			break
		}

		if inflightReq.Status == types.CancelledRequestStatus {
			pool.InflightRequestPool.Put(inflightReq)
			continue
		}

		inflightReq.Status = types.DoneRequestStatus
		inflightReq.Request.Callback(nil, errors.Wrap(err, "socket was closed"))
		metric.Collector.HandleUpstreamError(inflightReq.Request.Cmd, p.hostId)
	}

	p.poller.Delete(s.FD())
	s.Close()
}

func getSocketError(fd int) error {
	nerr, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	if err != nil {
		return err
	}

	if nerr == 0 {
		return nil
	}

	return unix.Errno(nerr)
}
