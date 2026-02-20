package socket

import (
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/ring"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"golang.org/x/sys/unix"
	"net"
)

const (
	DefaultRingSize = 8192
)

type Socket struct {
	fd int

	inflight *ring.SPSC[*types.InflightRequest]

	wantWrite bool

	rstart     int
	rend       int
	readBuffer []byte

	wstart      int
	wend        int
	writeBuffer []byte
}

func NewSocket(ctx *config.AppContext, addr string) (*Socket, error) {
	fd, err := dialFD(addr, int(ctx.Config.TimeoutNs/1e6))
	if err != nil {
		return nil, err
	}

	sock := &Socket{
		fd:          fd,
		readBuffer:  make([]byte, ctx.Config.BufferSize),
		writeBuffer: make([]byte, ctx.Config.BufferSize),
		inflight:    ring.NewSPSC[*types.InflightRequest](DefaultRingSize),
	}
	return sock, nil
}

func sockAddrFromAddrPort(addr string) (unix.Sockaddr, int, error) {
	ta, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, 0, err
	}

	if ip4 := ta.IP.To4(); ip4 != nil {
		var a [4]byte
		copy(a[:], ip4)
		return &unix.SockaddrInet4{Port: ta.Port, Addr: a}, unix.AF_INET, nil
	}

	ip16 := ta.IP.To16()
	if ip16 == nil {
		return nil, 0, unix.EINVAL
	}
	var a6 [16]byte
	copy(a6[:], ip16)

	zoneID := uint32(0)
	if ta.Zone != "" {
		ifi, _ := net.InterfaceByName(ta.Zone)
		if ifi != nil {
			zoneID = uint32(ifi.Index)
		}
	}
	return &unix.SockaddrInet6{Port: ta.Port, Addr: a6, ZoneId: zoneID}, unix.AF_INET6, nil
}

func waitConnect(fd int, timeoutMs int) error {
	pfds := []unix.PollFd{{
		Fd:     int32(fd),
		Events: unix.POLLOUT | unix.POLLERR | unix.POLLHUP,
	}}

	n, err := unix.Poll(pfds, timeoutMs)
	if err != nil {
		return err
	}

	if n == 0 {
		return unix.ETIMEDOUT
	}

	soerr, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	if err != nil {
		return err
	}

	if soerr != 0 {
		return unix.Errno(soerr)
	}

	return nil
}

func (s *Socket) FD() int {
	return s.fd
}

func (s *Socket) WantWrite() bool {
	return s.wantWrite
}

func (s *Socket) Discard(n int) {
	s.rstart += n

	if s.rstart == s.rend {
		s.rstart, s.rend = 0, 0
	} else if s.rstart > 0 {
		copy(s.readBuffer[0:], s.readBuffer[s.rstart:s.rend])
		s.rend -= s.rstart
		s.rstart = 0
	}
}

func (s *Socket) Read() ([]byte, error) {
	for {
		if s.rend == len(s.readBuffer) { //nolint
			break
		}

		n, err := unix.Read(s.fd, s.readBuffer[s.rend:])
		if n > 0 {
			s.rend += n
			continue
		}

		if n == 0 && err == nil {
			return nil, unix.ECONNRESET
		}

		if err == unix.EINTR {
			continue
		}

		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			break
		}

		if err != nil {
			return nil, err
		}
	}

	return s.readBuffer[s.rstart:s.rend], nil
}

func (s *Socket) Write(b []byte) bool {
	if len(b)+s.wend > len(s.writeBuffer) {
		return false
	}

	copy(s.writeBuffer[s.wend:], b)
	s.wend += len(b)
	s.wantWrite = true
	return true
}

func (s *Socket) Flush() error {
	for s.wstart < s.wend {
		n, err := unix.Write(s.fd, s.writeBuffer[s.wstart:s.wend])
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			s.wantWrite = true
			return nil
		}
		if err != nil {
			return err
		}
		s.wstart += n
	}

	s.wstart, s.wend = 0, 0
	s.wantWrite = false
	return nil
}

func (s *Socket) CanPushToInflight() bool {
	return s.inflight.CanPush()
}

func (s *Socket) PushToInflight(req *types.InflightRequest) {
	s.inflight.Push(req)
}

func (s *Socket) PopFromInflight() (*types.InflightRequest, bool) {
	return s.inflight.Pop()
}

func (s *Socket) Close() {
	_ = unix.Close(s.fd)
}
