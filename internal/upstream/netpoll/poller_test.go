package netpoll

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/upstream/internal/upstreamtest"
	"github.com/tentens-tech/gomcrouter/internal/upstream/socket"
	"golang.org/x/sys/unix"
)

func newTestPoller(t *testing.T) *Poller {
	t.Helper()
	p, err := NewPoller()
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

// newConn returns a connected upstream socket and the server side of it.
func newConn(t *testing.T) (*socket.Socket, net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	s, err := socket.NewSocket(upstreamtest.NewAppContext(t), ln.Addr().String())
	require.NoError(t, err)

	peer, err := ln.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	return s, peer
}

type events struct {
	r, w, e []*socket.Socket
}

func (ev *events) register(p *Poller, s *socket.Socket) {
	p.Register(s,
		func(s *socket.Socket) { ev.r = append(ev.r, s) },
		func(s *socket.Socket) { ev.w = append(ev.w, s) },
		func(s *socket.Socket) { ev.e = append(ev.e, s) },
	)
}

// pollUntil polls without blocking until cond holds.
func pollUntil(t *testing.T, p *Poller, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		require.True(t, time.Now().Before(deadline), "condition not met in time")
		require.NoError(t, p.Poll(10))
	}
}

func TestPoller_PollTimesOut(t *testing.T) {
	p := newTestPoller(t)

	start := time.Now()
	require.NoError(t, p.Poll(20))
	assert.GreaterOrEqual(t, time.Since(start), 15*time.Millisecond)
	assert.Equal(t, -1, p.nextSleepMsec, "an idle poll goes back to blocking sleep")
}

func TestPoller_WakeupInterruptsBlockingPoll(t *testing.T) {
	p := newTestPoller(t)

	done := make(chan error, 1)
	go func() { done <- p.Poll(-1) }()

	// the poller may not be blocked yet: keep waking it up until it returns
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = p.Wakeup()
				time.Sleep(time.Millisecond)
			}
		}
	}()

	var pollErr error
	select {
	case pollErr = <-done:
		close(stop)
		wg.Wait()
	case <-time.After(2 * time.Second):
		close(stop)
		wg.Wait()
		require.FailNow(t, "wakeup did not interrupt the blocking poll")
	}
	require.NoError(t, pollErr)

	// the producer may have woken the poller again after the blocking poll
	// returned: consume that wakeup before checking the state
	require.NoError(t, p.Poll(0))

	// a wakeup is consumed (pipe drained, flag reset) by the poll it ended
	assert.Zero(t, atomic.LoadInt32(&p.wakeupCall))
	var buf [8]byte
	_, err := unix.Read(p.eventFD.r, buf[:])
	require.ErrorIs(t, err, unix.EAGAIN)
}

func TestPoller_WakeupIsCoalesced(t *testing.T) {
	p := newTestPoller(t)

	for range 10 {
		require.NoError(t, p.Wakeup())
	}

	var buf [16]byte
	n, err := unix.Read(p.eventFD.r, buf[:])
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the first wakeup writes to the pipe")

	// after a poll, the next wakeup triggers again
	require.NoError(t, p.Poll(0))
	require.NoError(t, p.Wakeup())
	n, err = unix.Read(p.eventFD.r, buf[:])
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestPoller_PollSleepCap(t *testing.T) {
	p := newTestPoller(t)

	// after events the poller does not sleep, whatever the cap
	p.nextSleepMsec = 0
	start := time.Now()
	require.NoError(t, p.Poll(1000))
	assert.Less(t, time.Since(start), 500*time.Millisecond)

	// a negative cap keeps the poller's own sleep
	p.nextSleepMsec = 0
	require.NoError(t, p.Poll(-1))
}

func TestPoller_ReadWriteEvents(t *testing.T) {
	p := newTestPoller(t)
	s, peer := newConn(t)
	t.Cleanup(s.Close)

	ev := &events{}
	ev.register(p, s)

	// write interest is off by default
	require.NoError(t, p.Poll(0))
	assert.Empty(t, ev.w)

	// enable write interest
	require.True(t, s.Write([]byte("version\r\n")))
	require.NoError(t, p.Mod(s))
	pollUntil(t, p, func() bool { return len(ev.w) > 0 })
	assert.Same(t, s, ev.w[0])
	require.NoError(t, s.Flush())

	// disable it again
	require.NoError(t, p.Mod(s))
	ev.w = nil
	require.NoError(t, p.Poll(0))
	assert.Empty(t, ev.w)

	// readable once the peer answers
	_, err := peer.Write([]byte("VERSION 1\r\n"))
	require.NoError(t, err)
	pollUntil(t, p, func() bool { return len(ev.r) > 0 })
	assert.Same(t, s, ev.r[0])
	assert.Empty(t, ev.e)
}

func TestPoller_NoWriteCallbackAfterTheReadCallbackReplacedTheSocket(t *testing.T) {
	p := newTestPoller(t)
	s, peer := newConn(t)
	t.Cleanup(s.Close)
	repl, _ := newConn(t)
	t.Cleanup(repl.Close)

	var writes []*socket.Socket
	replaced := false
	p.Register(s,
		func(s *socket.Socket) {
			// like PollGroup.recreateSocket
			p.Delete(s.FD())
			p.Register(repl, func(*socket.Socket) {}, func(w *socket.Socket) { writes = append(writes, w) }, func(*socket.Socket) {})
			replaced = true
		},
		func(w *socket.Socket) {
			if replaced {
				writes = append(writes, w)
			}
		},
		func(*socket.Socket) {},
	)

	// readable and writable in the same poll
	require.True(t, s.Write([]byte("version\r\n")))
	require.NoError(t, p.Mod(s))
	_, err := peer.Write([]byte("VERSION 1\r\n"))
	require.NoError(t, err)
	pollUntil(t, p, func() bool { return replaced })

	for _, w := range writes {
		assert.NotSame(t, s, w, "the write callback ran on the replaced socket")
	}
}

func TestPoller_ErrorEvent(t *testing.T) {
	p := newTestPoller(t)
	s, _ := newConn(t)

	ev := &events{}
	ev.register(p, s)

	// a closed fd is reported as POLLNVAL
	s.Close()
	pollUntil(t, p, func() bool { return len(ev.e) > 0 })
	assert.Same(t, s, ev.e[0])
	assert.Empty(t, ev.r)
	assert.Empty(t, ev.w)

	p.Delete(s.FD())
}

func TestPoller_ModAndDelete(t *testing.T) {
	p := newTestPoller(t)
	a, _ := newConn(t)
	b, peerB := newConn(t)
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)

	evA, evB := &events{}, &events{}
	evA.register(p, a)
	evB.register(p, b)
	require.Len(t, p.pfds, 3)
	require.Len(t, p.attachments, 2)

	// events are routed to the right socket
	_, err := peerB.Write([]byte("END\r\n"))
	require.NoError(t, err)
	pollUntil(t, p, func() bool { return len(evB.r) > 0 })
	assert.Empty(t, evA.r)
	b.Discard(len(readAll(t, b)))
	evB.r = nil

	p.Delete(a.FD())
	require.Len(t, p.pfds, 2)
	require.Len(t, p.attachments, 1)
	require.ErrorIs(t, p.Mod(a), unix.ENOENT)

	// unknown fd: no-op
	p.Delete(a.FD())
	require.Len(t, p.pfds, 2)

	// the remaining socket keeps its callbacks
	_, err = peerB.Write([]byte("END\r\n"))
	require.NoError(t, err)
	pollUntil(t, p, func() bool { return len(evB.r) > 0 })
	assert.Empty(t, evA.r)
	require.NoError(t, p.Mod(b))
}

func TestEventFD(t *testing.T) {
	t.Run("trigger on a full pipe does not fail", func(t *testing.T) {
		e, err := newEventFD()
		require.NoError(t, err)
		t.Cleanup(e.close)

		for {
			if _, err := unix.Write(e.w, WakeupData); err != nil {
				require.ErrorIs(t, err, unix.EAGAIN)
				break
			}
		}
		require.NoError(t, e.trigger())
	})

	t.Run("trigger on a closed pipe fails", func(t *testing.T) {
		e, err := newEventFD()
		require.NoError(t, err)
		e.close()
		require.ErrorIs(t, e.trigger(), unix.EBADF)
	})

	t.Run("drain empties the pipe", func(t *testing.T) {
		e, err := newEventFD()
		require.NoError(t, err)
		t.Cleanup(e.close)

		require.NoError(t, e.trigger())
		e.drain()
		e.drain() // empty: no-op

		var buf [1]byte
		_, err = unix.Read(e.r, buf[:])
		require.ErrorIs(t, err, unix.EAGAIN)
	})
}

func readAll(t *testing.T, s *socket.Socket) []byte {
	t.Helper()
	b, err := s.Read()
	require.NoError(t, err)
	return b
}
