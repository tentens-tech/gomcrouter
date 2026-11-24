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

	// ErrConnectionClosed is reported when the poller signals a hangup or an
	// error on a socket that carries no pending SO_ERROR (e.g. the peer closed
	// the connection, which macOS reports as POLLHUP).
	ErrConnectionClosed = errors.New("connection closed")

	// ErrRequestTooLarge and ErrResponseTooLarge are reported for messages
	// that do not fit into the socket buffers (see the buffer-size flag).
	ErrRequestTooLarge  = errors.New("request does not fit into the socket buffer")
	ErrResponseTooLarge = errors.New("response does not fit into the socket buffer")
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
		// the metric first: the callback may return the request to its pool
		metric.Collector.HandleUpstreamError(req.Cmd, p.hostId)
		req.Callback(nil, ErrClosed)
		return false
	}

	if !p.requestRing.Push(req) {
		// the metric first: the callback may return the request to its pool
		metric.Collector.HandleUpstreamError(req.Cmd, p.hostId)
		req.Callback(nil, ErrBusy)
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
		// the metric first: the callback may return the request to its pool
		metric.Collector.HandleUpstreamError(r.Request.Cmd, p.hostId)
		r.Request.Callback(nil, ErrTimeout)
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

// capSleep lowers msec (the event loop's poll sleep, -1 meaning infinite) so
// that the loop wakes up no later than the earliest pending request deadline
// and can expire it even when no socket event arrives.
func (p *PollGroup) capSleep(msec int) int {
	ts, ok := p.timeoutHeap.PeekTS()
	if !ok {
		return msec
	}

	untilMsec := (ts - time.NanoTime() + 999_999) / 1_000_000 // round up
	if untilMsec < 0 {
		untilMsec = 0
	}
	if msec < 0 || untilMsec < int64(msec) {
		return int(untilMsec)
	}
	return msec
}

func (p *PollGroup) dispatchOnSocket(s *socket.Socket) {
	now := time.NanoTime()
	// i counts the requests written: rejected oversized requests don't use the
	// batch, so a run of them can't leave the next requests waiting for a
	// wakeup that never comes
	for i := 0; i < SocketIOBatch; {
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
			if len(req.Raw.B) > s.BufferSize() {
				// can never be written: fail it instead of blocking the ring
				_ = p.requestRing.Discard()
				// the metric first: the callback may return the request to its pool
				metric.Collector.HandleUpstreamError(req.Cmd, p.hostId)
				req.Callback(nil, ErrRequestTooLarge)
				continue
			}
			return
		}

		in := pool.InflightRequestPool.Get().(*types.InflightRequest) //nolint:errcheck // the pool only holds *types.InflightRequest
		in.Request = req
		in.Ts = now
		in.Status = types.InflightRequestStatus

		_ = p.requestRing.Discard()
		s.PushToInflight(in)

		p.timeoutHeap.Push(now+p.ctx.Config.TimeoutNs, in)
		i++
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
			if offset == 0 && len(b) == s.BufferSize() {
				// a full buffer without a complete response would never make
				// progress (and keeps the socket readable: busy loop)
				p.recreateSocket(s, ErrResponseTooLarge)
				return
			}
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
	err := getSocketError(s.FD())
	if err == nil {
		// never fail inflight requests with a nil error: callbacks would get
		// (nil, nil) and dereference the nil response.
		err = ErrConnectionClosed
	}
	p.recreateSocket(s, err)
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

		// the metric first: the callback may return the request to its pool
		metric.Collector.HandleUpstreamError(req.Cmd, p.hostId)
		req.Callback(nil, errors.Wrap(err, "poll group was closed"))
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
		// the metric first: the callback may return the request to its pool
		metric.Collector.HandleUpstreamError(inflightReq.Request.Cmd, p.hostId)
		inflightReq.Request.Callback(nil, errors.Wrap(err, "socket was closed"))
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
