package socket

import (
	"bufio"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/internal/upstream/internal/upstreamtest"
	"golang.org/x/sys/unix"
)

func loopbackInterface(t *testing.T) net.Interface {
	t.Helper()
	ifs, err := net.Interfaces()
	require.NoError(t, err)
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagLoopback != 0 {
			return ifi
		}
	}
	t.Skip("no loopback interface")
	return net.Interface{}
}

func TestSockAddrFromAddrPort(t *testing.T) {
	lo := loopbackInterface(t)

	tests := []struct {
		name       string
		addr       string
		wantFamily int
		wantSA     unix.Sockaddr
		wantErr    bool
	}{
		{
			name:       "ipv4",
			addr:       "127.0.0.1:11211",
			wantFamily: unix.AF_INET,
			wantSA:     &unix.SockaddrInet4{Port: 11211, Addr: [4]byte{127, 0, 0, 1}},
		},
		{
			name:       "ipv4-mapped ipv6 is dialled as ipv4",
			addr:       "[::ffff:10.1.2.3]:80",
			wantFamily: unix.AF_INET,
			wantSA:     &unix.SockaddrInet4{Port: 80, Addr: [4]byte{10, 1, 2, 3}},
		},
		{
			name:       "ipv6",
			addr:       "[::1]:11211",
			wantFamily: unix.AF_INET6,
			wantSA:     &unix.SockaddrInet6{Port: 11211, Addr: [16]byte{15: 1}},
		},
		{
			name:       "ipv6 with interface zone",
			addr:       "[fe80::1%" + lo.Name + "]:11211",
			wantFamily: unix.AF_INET6,
			wantSA: &unix.SockaddrInet6{
				Port:   11211,
				Addr:   [16]byte{0: 0xfe, 1: 0x80, 15: 1},
				ZoneId: uint32(lo.Index), // #nosec G115
			},
		},
		{
			name:       "ipv6 with unknown zone",
			addr:       "[fe80::1%no-such-if0]:11211",
			wantFamily: unix.AF_INET6,
			wantSA:     &unix.SockaddrInet6{Port: 11211, Addr: [16]byte{0: 0xfe, 1: 0x80, 15: 1}},
		},
		{name: "missing port", addr: "127.0.0.1", wantErr: true},
		{name: "bad port", addr: "127.0.0.1:http-ish", wantErr: true},
		{name: "port out of range", addr: "127.0.0.1:70000", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sa, family, err := sockAddrFromAddrPort(tt.addr)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFamily, family)
			assert.Equal(t, tt.wantSA, sa)
		})
	}
}

func TestSockAddrFromAddrPort_NoIP(t *testing.T) {
	// a missing host resolves to a nil IP, which is neither v4 nor v6
	_, _, err := sockAddrFromAddrPort(":11211")
	require.ErrorIs(t, err, unix.EINVAL)
}

func TestDialFD(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		srv := upstreamtest.NewServer(t)

		fd, err := dialFD(srv.Addr(), 1000)
		require.NoError(t, err)
		t.Cleanup(func() { _ = unix.Close(fd) })

		assertSocketOptions(t, fd)
		require.Eventually(t, func() bool { return srv.Accepted() == 1 }, time.Second, time.Millisecond)
	})

	t.Run("ipv6", func(t *testing.T) {
		ln, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Skipf("ipv6 loopback unavailable: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })

		fd, err := dialFD(ln.Addr().String(), 1000)
		require.NoError(t, err)
		t.Cleanup(func() { _ = unix.Close(fd) })

		assertSocketOptions(t, fd)
		c, err := ln.Accept()
		require.NoError(t, err)
		require.NoError(t, c.Close())
	})

	t.Run("refused", func(t *testing.T) {
		_, err := dialFD(upstreamtest.RefusedAddr(t), 1000)
		require.ErrorIs(t, err, unix.ECONNREFUSED)
	})

	t.Run("connect fails at once", func(t *testing.T) {
		// TCP to a multicast address is rejected synchronously
		// (EAFNOSUPPORT on BSD, ENETUNREACH on Linux)
		_, err := dialFD("224.0.0.1:11211", 1000)
		require.Error(t, err)
	})

	t.Run("timeout", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("only Linux silently drops SYNs once the accept queue is full; " +
				"the timeout itself is covered by TestWaitConnect")
		}

		// a listener that never accepts, with the smallest backlog
		lfd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = unix.Close(lfd) })
		require.NoError(t, unix.Bind(lfd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}))
		require.NoError(t, unix.Listen(lfd, 0))
		sa, err := unix.Getsockname(lfd)
		require.NoError(t, err)
		sa4, ok := sa.(*unix.SockaddrInet4)
		require.True(t, ok)
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(sa4.Port))

		for range 64 {
			start := time.Now()
			fd, err := dialFD(addr, 50)
			if err == nil {
				t.Cleanup(func() { _ = unix.Close(fd) })
				continue
			}
			require.ErrorIs(t, err, unix.ETIMEDOUT)
			assert.GreaterOrEqual(t, time.Since(start), 45*time.Millisecond)
			return
		}
		require.Fail(t, "accept queue never filled up")
	})

	t.Run("bad address", func(t *testing.T) {
		_, err := dialFD("not an address", 1000)
		require.Error(t, err)
	})
}

func assertSocketOptions(t *testing.T, fd int) {
	t.Helper()

	nodelay, err := unix.GetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_NODELAY)
	require.NoError(t, err)
	assert.NotZero(t, nodelay, "TCP_NODELAY")

	fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	require.NoError(t, err)
	assert.NotZero(t, fl&unix.O_NONBLOCK, "O_NONBLOCK")

	fdfl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, fdfl&unix.FD_CLOEXEC, "FD_CLOEXEC")
}

func pipe(t *testing.T) (r, w int) {
	t.Helper()
	var fds [2]int
	require.NoError(t, unix.Pipe(fds[:]))
	t.Cleanup(func() {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
	})
	return fds[0], fds[1]
}

func TestWaitConnect(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		// the read end of a pipe never becomes writable
		r, _ := pipe(t)
		start := time.Now()
		require.ErrorIs(t, waitConnect(r, 30), unix.ETIMEDOUT)
		assert.GreaterOrEqual(t, time.Since(start), 25*time.Millisecond)
	})

	t.Run("not a socket", func(t *testing.T) {
		// the write end is writable at once, but has no SO_ERROR
		_, w := pipe(t)
		require.ErrorIs(t, waitConnect(w, 1000), unix.ENOTSOCK)
	})

	t.Run("bad fd", func(t *testing.T) {
		// poll reports POLLNVAL for a closed fd: SO_ERROR lookup fails
		var fds [2]int
		require.NoError(t, unix.Pipe(fds[:]))
		require.NoError(t, unix.Close(fds[0]))
		require.NoError(t, unix.Close(fds[1]))
		require.ErrorIs(t, waitConnect(fds[1], 1000), unix.EBADF)
	})
}

func newTestSocket(t *testing.T, opts ...upstreamtest.Options) (*Socket, net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	ctx := upstreamtest.NewAppContext(t, opts...)
	s, err := NewSocket(ctx, ln.Addr().String())
	require.NoError(t, err)
	t.Cleanup(s.Close)

	peer, err := ln.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	return s, peer
}

func TestNewSocket(t *testing.T) {
	s, _ := newTestSocket(t, func(c *config.App) { c.BufferSize = 4096 })

	assert.Positive(t, s.FD())
	assert.Equal(t, 4096, s.BufferSize())
	assert.False(t, s.WantWrite())
	assert.True(t, s.CanPushToInflight())
}

func TestNewSocket_Refused(t *testing.T) {
	ctx := upstreamtest.NewAppContext(t)
	s, err := NewSocket(ctx, upstreamtest.RefusedAddr(t))
	require.ErrorIs(t, err, unix.ECONNREFUSED)
	assert.Nil(t, s)
}

// readAtLeast reads until the socket buffer holds at least n bytes.
func readAtLeast(t *testing.T, s *Socket, n int) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, err := s.Read()
		require.NoError(t, err)
		if len(b) >= n {
			return b
		}
		require.True(t, time.Now().Before(deadline), "got %q", b)
		waitReadable(t, s.FD())
	}
}

// waitReadable blocks until fd has data (or a hangup) to read.
func waitReadable(t *testing.T, fd int) {
	t.Helper()
	pfds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}} // #nosec G115
	n, err := unix.Poll(pfds, 2000)
	require.NoError(t, err)
	require.Equal(t, 1, n, "fd did not become readable")
}

func TestSocket_WriteFlush(t *testing.T) {
	s, peer := newTestSocket(t, func(c *config.App) { c.BufferSize = 16 })

	require.True(t, s.Write([]byte("get a\r\n")))
	require.True(t, s.Write([]byte("get b\r\n")))
	assert.True(t, s.WantWrite())
	assert.False(t, s.Write([]byte("get c\r\n")), "does not fit into the buffer")

	require.NoError(t, s.Flush())
	assert.False(t, s.WantWrite())

	r := bufio.NewReader(peer)
	for _, want := range []string{"get a\r\n", "get b\r\n"} {
		line, err := r.ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, want, line)
	}

	// the buffer is reusable after a flush
	assert.True(t, s.Write([]byte("get c\r\n")))
	require.NoError(t, s.Flush())
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "get c\r\n", line)

	// nothing to flush
	require.NoError(t, s.Flush())
}

func TestSocket_FlushWouldBlock(t *testing.T) {
	const size = 8 << 20
	s, peer := newTestSocket(t, func(c *config.App) { c.BufferSize = size })

	// the peer does not read: the kernel buffers fill up long before 8MiB
	require.True(t, s.Write(make([]byte, size)))
	require.NoError(t, s.Flush())
	assert.True(t, s.WantWrite(), "the rest is kept for the next writable event")

	// once the peer reads, the rest can be flushed
	go func() {
		buf := make([]byte, 64*1024)
		total := 0
		for total < size {
			n, err := peer.Read(buf)
			if err != nil {
				return
			}
			total += n
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for s.WantWrite() {
		require.True(t, time.Now().Before(deadline), "flush did not complete")
		require.NoError(t, s.Flush())
		time.Sleep(time.Millisecond)
	}
}

func TestSocket_FlushError(t *testing.T) {
	s, _ := newTestSocket(t)

	require.NoError(t, unix.Shutdown(s.FD(), unix.SHUT_WR))
	require.True(t, s.Write([]byte("get a\r\n")))
	require.ErrorIs(t, s.Flush(), unix.EPIPE)
}

func TestSocket_ReadDiscard(t *testing.T) {
	s, peer := newTestSocket(t, func(c *config.App) { c.BufferSize = 32 })

	// nothing to read yet
	b, err := s.Read()
	require.NoError(t, err)
	assert.Empty(t, b)

	_, err = peer.Write([]byte("END\r\nSTORED\r\n"))
	require.NoError(t, err)
	b = readAtLeast(t, s, len("END\r\nSTORED\r\n"))
	assert.Equal(t, "END\r\nSTORED\r\n", string(b))

	// partial discard keeps (and compacts) the rest
	s.Discard(len("END\r\n"))
	b, err = s.Read()
	require.NoError(t, err)
	assert.Equal(t, "STORED\r\n", string(b))

	// full discard resets the buffer
	s.Discard(len("STORED\r\n"))
	assert.Zero(t, s.rstart)
	assert.Zero(t, s.rend)
}

func TestSocket_ReadStopsWhenBufferIsFull(t *testing.T) {
	s, peer := newTestSocket(t, func(c *config.App) { c.BufferSize = 8 })

	_, err := peer.Write([]byte(strings.Repeat("x", 20)))
	require.NoError(t, err)
	require.Len(t, readAtLeast(t, s, 8), 8)

	// buffer full: no further read, the rest stays in the kernel
	b, err := s.Read()
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("x", 8), string(b))

	s.Discard(8)
	b, err = s.Read()
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("x", 8), string(b))
}

func TestSocket_ReadPeerClosed(t *testing.T) {
	s, peer := newTestSocket(t)

	require.NoError(t, peer.Close())
	waitReadable(t, s.FD())

	_, err := s.Read()
	require.ErrorIs(t, err, unix.ECONNRESET)
}

func TestSocket_ReadError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	s, err := NewSocket(upstreamtest.NewAppContext(t), ln.Addr().String())
	require.NoError(t, err)
	s.Close()

	_, err = s.Read()
	require.ErrorIs(t, err, unix.EBADF)
}

func TestSocket_Inflight(t *testing.T) {
	s, _ := newTestSocket(t)

	_, ok := s.PopFromInflight()
	assert.False(t, ok)

	reqs := make([]*types.InflightRequest, DefaultRingSize)
	for i := range reqs {
		require.True(t, s.CanPushToInflight())
		reqs[i] = &types.InflightRequest{Ts: int64(i)}
		s.PushToInflight(reqs[i])
	}
	assert.False(t, s.CanPushToInflight())

	for i := range reqs {
		got, popped := s.PopFromInflight()
		require.True(t, popped)
		require.Same(t, reqs[i], got, "FIFO order")
	}
	_, ok = s.PopFromInflight()
	assert.False(t, ok)
}
