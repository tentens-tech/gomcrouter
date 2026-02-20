package netpoll

import (
	"github.com/tentens-tech/gomcrouter/internal/upstream/socket"
	"golang.org/x/sys/unix"
	"runtime"
	"sync/atomic"
)

var (
	ReadEvents      int16 = unix.POLLIN | unix.POLLERR | unix.POLLHUP
	ReadWriteEvents       = unix.POLLOUT | ReadEvents
)

type attachment struct {
	sock *socket.Socket
	onR  func(sock *socket.Socket)
	onW  func(sock *socket.Socket)
	onE  func(sock *socket.Socket)
}

type Poller struct {
	nextSleepMsec int
	wakeupCall    int32

	eventFD     *eventFD
	pfds        []unix.PollFd
	attachments []*attachment
}

func NewPoller() (*Poller, error) {
	efd, err := newEventFD()
	if err != nil {
		return nil, err
	}

	p := &Poller{
		nextSleepMsec: -1,
		eventFD:       efd,
		pfds: []unix.PollFd{{
			Fd:     int32(efd.r),
			Events: unix.POLLIN,
		}},
	}

	return p, nil
}

func (p *Poller) Wakeup() (err error) {
	if atomic.CompareAndSwapInt32(&p.wakeupCall, 0, 1) {
		err = p.eventFD.trigger()
	}
	return err
}

func (p *Poller) Close() {
	p.eventFD.close()
}

func (p *Poller) Register(sock *socket.Socket, onR, onW, onE func(sock *socket.Socket)) {
	pfd := unix.PollFd{
		Fd:     int32(sock.FD()),
		Events: ReadEvents,
	}
	p.pfds = append(p.pfds, pfd)
	p.attachments = append(p.attachments, &attachment{
		sock: sock,
		onR:  onR,
		onW:  onW,
		onE:  onE,
	})
}

func (p *Poller) Mod(sock *socket.Socket) (err error) {
	for i := 1; i < len(p.pfds); i++ {
		if p.pfds[i].Fd != int32(sock.FD()) {
			continue
		}

		if sock.WantWrite() {
			p.pfds[i].Events = ReadWriteEvents
		} else {
			p.pfds[i].Events = ReadEvents
		}

		return nil
	}

	return unix.ENOENT
}

func (p *Poller) Delete(fd int) {
	for i := 1; i < len(p.pfds); i++ {
		if p.pfds[i].Fd != int32(fd) {
			continue
		}
		attachmentIdx := i - 1
		p.attachments = append(p.attachments[:attachmentIdx], p.attachments[attachmentIdx+1:]...)
		p.pfds = append(p.pfds[:i], p.pfds[i+1:]...)
		return
	}
}

func (p *Poller) Poll() error {
	n, err := unix.Poll(p.pfds, p.nextSleepMsec)
	if n == 0 || err == unix.EINTR {
		atomic.StoreInt32(&p.wakeupCall, 0)
		p.nextSleepMsec = -1
		runtime.Gosched()
		return nil
	} else if err != nil {
		return err
	}

	p.nextSleepMsec = 0
	atomic.StoreInt32(&p.wakeupCall, 0)

	if p.pfds[0].Revents&(unix.POLLIN|unix.POLLERR|unix.POLLHUP) != 0 {
		p.eventFD.drain()
		n--
	}

	for i := 0; i < len(p.attachments) && n > 0; i++ {
		re := p.pfds[i+1].Revents
		if re == 0 {
			continue
		}
		n--

		pa := p.attachments[i]

		if re&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			pa.onE(pa.sock)
			continue
		}

		if re&unix.POLLIN != 0 {
			pa.onR(pa.sock)
		}

		if re&unix.POLLOUT != 0 {
			pa.onW(pa.sock)
		}
	}

	return nil
}
