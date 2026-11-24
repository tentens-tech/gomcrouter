package io

import (
	"errors"
	"fmt"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/machinery/registry"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/upstream/internal/upstreamtest"
	"golang.org/x/sys/unix"
)

func oneConn(c *config.App) { c.UpstreamConnections = 1 }

func TestPollGroup_Accessors(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) { c.UpstreamConnections = 4 })
	pg, _, _ := newTestPollGroup(t, ctx, srv.Addr())

	assert.True(t, pg.IsActive())
	assert.Equal(t, 4, pg.NumSockets())
	assert.Equal(t, srv.Addr(), registry.GlobalHostRegistry.GetHost(pg.HostId()))
	require.Eventually(t, func() bool { return srv.ActiveConns() == 4 }, waitFor, time.Millisecond)
}

func TestPollGroup_InitFailure(t *testing.T) {
	ctx := upstreamtest.NewAppContext(t)
	addr := upstreamtest.RefusedAddr(t)

	pg, err := newPollGroup(addr, hostID(addr), ctx, nil, nil, nil)
	require.Error(t, err)
	assert.Nil(t, pg)
}

func TestPollGroup_EnqueueOnClosedGroup(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, _, _ := newTestPollGroup(t, ctx, srv.Addr())

	pg.Drain()
	assert.False(t, pg.IsActive())
	assert.Zero(t, pg.NumSockets())

	req, ch := getReq("k")
	assert.False(t, pg.Enqueue(req))
	r := await(t, ch)
	require.ErrorIs(t, r.err, ErrClosed)
	assert.Nil(t, r.resp)
}

func TestPollGroup_EnqueueWhenRingIsFull(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, _, _ := newTestPollGroup(t, ctx, srv.Addr())

	for range DefaultRingSize {
		req, _ := getReq("k")
		require.True(t, pg.Enqueue(req))
	}

	req, ch := getReq("k")
	assert.False(t, pg.Enqueue(req))
	require.ErrorIs(t, await(t, ch).err, ErrBusy)
}

func TestPollGroup_DrainFailsQueuedAndInflightRequests(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetMode(upstreamtest.ModeSilent)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, poller, _ := newTestPollGroup(t, ctx, srv.Addr())

	inflight, inflightCh := getReq("a")
	require.True(t, pg.Enqueue(inflight))
	pump(t, pg, poller, func() bool { return srv.Requests() == 1 })

	queued, queuedCh := getReq("b")
	require.True(t, pg.Enqueue(queued))

	pg.Drain()

	for _, ch := range []chan result{inflightCh, queuedCh} {
		r := await(t, ch)
		require.ErrorIs(t, r.err, ErrClosedByClient)
		assert.Nil(t, r.resp)
		requireNoMore(t, ch)
	}
	require.Eventually(t, func() bool { return srv.ActiveConns() == 0 }, waitFor, time.Millisecond)
}

func TestPollGroup_Responses(t *testing.T) {
	big := strings.Repeat("x", 100*1024)

	tests := []struct {
		name string
		cmd  int
		raw  string
		want string
	}{
		{name: "get miss", cmd: ascii.GetCmd, raw: "get nope\r\n", want: "END\r\n"},
		{name: "get hit", cmd: ascii.GetCmd, raw: "get k\r\n", want: "VALUE k 0 1\r\nv\r\nEND\r\n"},
		{name: "multi get", cmd: ascii.GetCmd, raw: "get k nope k\r\n", want: "VALUE k 0 1\r\nv\r\nVALUE k 0 1\r\nv\r\nEND\r\n"},
		{name: "gets", cmd: ascii.GetsCmd, raw: "gets k\r\n", want: "VALUE k 0 1 1\r\nv\r\nEND\r\n"},
		{
			name: "value larger than a single socket read",
			cmd:  ascii.GetCmd,
			raw:  "get big\r\n",
			want: "VALUE big 0 102400\r\n" + big + "\r\nEND\r\n",
		},
		{name: "set", cmd: ascii.SetCmd, raw: "set n 0 0 2\r\nab\r\n", want: "STORED\r\n"},
		{name: "delete", cmd: ascii.DeleteCmd, raw: "delete k2\r\n", want: "DELETED\r\n"},
		{name: "version", cmd: ascii.VersionCmd, raw: "version\r\n", want: "VERSION 1.6.0-fake\r\n"},
		{name: "unknown command", cmd: ascii.QuitCmd, raw: "bogus\r\n", want: "ERROR\r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := upstreamtest.NewServer(t)
			srv.Set("k", []byte("v"))
			srv.Set("k2", []byte("v"))
			srv.Set("big", []byte(big))
			ctx := upstreamtest.NewAppContext(t, func(c *config.App) {
				c.UpstreamConnections = 1
				c.BufferSize = 256 * 1024
			})
			pg, poller, h := newTestPollGroup(t, ctx, srv.Addr())

			req, ch := newReq(tt.cmd, tt.raw)
			require.True(t, pg.Enqueue(req))

			var r result
			pump(t, pg, poller, received(ch, &r))
			require.NoError(t, r.err)
			assert.Equal(t, tt.want, string(r.resp))
			assert.Zero(t, h.failures)
		})
	}
}

func TestPollGroup_ManyResponsesInOneRead(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, poller, _ := newTestPollGroup(t, ctx, srv.Addr())

	const n = 50
	chans := make([]chan result, n)
	for i := range n {
		srv.Set(fmt.Sprint(i), []byte(fmt.Sprint(i)))
		req, ch := getReq(fmt.Sprint(i))
		chans[i] = ch
		require.True(t, pg.Enqueue(req))
	}

	got := 0
	pump(t, pg, poller, func() bool {
		for got < n {
			select {
			case r := <-chans[got]:
				require.NoError(t, r.err)
				require.Equal(t, fmt.Sprintf("VALUE %d 0 %d\r\n%d\r\nEND\r\n", got, len(fmt.Sprint(got)), got), string(r.resp))
				got++
			default:
				return false
			}
		}
		return true
	})
}

func TestPollGroup_TimeoutAndLateResponse(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetDelay(60 * time.Millisecond)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) {
		c.UpstreamConnections = 1
		c.TimeoutMillis = 20
	})
	pg, poller, h := newTestPollGroup(t, ctx, srv.Addr())

	req, ch := getReq("k")
	start := time.Now()
	require.True(t, pg.Enqueue(req))

	var r result
	pump(t, pg, poller, received(ch, &r))
	require.ErrorIs(t, r.err, ErrTimeout)
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)

	// the late response is read and dropped without a second callback and
	// without breaking the connection
	pump(t, pg, poller, func() bool { return time.Since(start) > 100*time.Millisecond })
	requireNoMore(t, ch)
	assert.Zero(t, h.failures)
	assert.Equal(t, 1, srv.Accepted())

	// successful requests stay in the timeout heap until their deadline and
	// are recycled then, without a callback
	srv.SetDelay(0)
	req, ch = getReq("k")
	require.True(t, pg.Enqueue(req))
	pump(t, pg, poller, received(ch, &r))
	require.NoError(t, r.err)
	assert.Equal(t, 1, pg.timeoutHeap.Len())
	pump(t, pg, poller, func() bool { return pg.timeoutHeap.Len() == 0 })
	requireNoMore(t, ch)
}

func TestPollGroup_CapSleep(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetMode(upstreamtest.ModeSilent)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) {
		c.UpstreamConnections = 1
		c.TimeoutMillis = 1000
	})
	pg, _, _ := newTestPollGroup(t, ctx, srv.Addr())

	// no pending request: the sleep is left as is
	assert.Equal(t, -1, pg.capSleep(-1))
	assert.Equal(t, 5, pg.capSleep(5))

	req, _ := getReq("k")
	require.True(t, pg.Enqueue(req))
	pg.dispatch()

	got := pg.capSleep(-1)
	assert.InDelta(t, 1000, got, 100, "infinite sleep is capped by the request deadline")
	assert.Equal(t, 5, pg.capSleep(5), "shorter sleeps are kept")
	// each call samples the clock, so a later one may be a millisecond (or
	// more on a slow machine) shorter, never longer
	longer := pg.capSleep(5000)
	assert.LessOrEqual(t, longer, got, "longer sleeps are capped by the request deadline")
	assert.InDelta(t, 1000, longer, 100)

	// overdue deadline: do not sleep at all
	pg.timeoutHeap.Push(1, nil)
	assert.Zero(t, pg.capSleep(-1))
}

func TestPollGroup_RecreatesSocket(t *testing.T) {
	tests := []struct {
		name    string
		trigger func(srv *upstreamtest.Server)
		wantErr string
	}{
		{
			name: "malformed response",
			trigger: func(srv *upstreamtest.Server) {
				srv.SetMode(upstreamtest.ModeMalformed)
			},
			wantErr: "ascii decode response failed",
		},
		{
			name: "connection closed by server",
			trigger: func(srv *upstreamtest.Server) {
				srv.SetMode(upstreamtest.ModeClose)
			},
			wantErr: "socket was closed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := upstreamtest.NewServer(t)
			ctx := upstreamtest.NewAppContext(t, oneConn)
			pg, poller, h := newTestPollGroup(t, ctx, srv.Addr())
			oldSock := pg.sockets[0]

			tt.trigger(srv)
			req, ch := getReq("k")
			require.True(t, pg.Enqueue(req))

			var r result
			pump(t, pg, poller, received(ch, &r))
			require.ErrorContains(t, r.err, tt.wantErr)
			assert.Nil(t, r.resp)
			requireNoMore(t, ch)

			// the broken socket was replaced in place by a fresh connection
			require.Len(t, pg.sockets, 1)
			assert.NotSame(t, oldSock, pg.sockets[0])
			require.Eventually(t, func() bool { return srv.Accepted() == 2 }, waitFor, time.Millisecond)

			srv.SetMode(upstreamtest.ModeNormal)
			req, ch = getReq("k")
			require.True(t, pg.Enqueue(req))
			pump(t, pg, poller, received(ch, &r))
			require.NoError(t, r.err)
			assert.Equal(t, "END\r\n", string(r.resp))
			assert.Zero(t, h.failures)
		})
	}
}

func TestPollGroup_WriteFailureRecreatesSocket(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, poller, h := newTestPollGroup(t, ctx, srv.Addr())
	sock := pg.sockets[0]

	req, ch := getReq("k")
	require.True(t, pg.Enqueue(req))
	pg.dispatchOnSocket(sock)
	require.True(t, sock.WantWrite())

	// driven by hand: with the poller the read side (FIN from the server)
	// could win the race against the write
	require.NoError(t, unix.Shutdown(sock.FD(), unix.SHUT_WR))
	pg.onWriteable(sock)

	r := await(t, ch)
	require.ErrorIs(t, r.err, unix.EPIPE)
	require.Len(t, pg.sockets, 1)
	assert.NotSame(t, sock, pg.sockets[0])
	assert.Zero(t, h.failures)

	req, ch = getReq("k")
	require.True(t, pg.Enqueue(req))
	pump(t, pg, poller, received(ch, &r))
	require.NoError(t, r.err)
}

func TestPollGroup_UnsolicitedResponseRecreatesSocket(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, poller, h := newTestPollGroup(t, ctx, srv.Addr())
	require.Eventually(t, func() bool { return srv.ActiveConns() == 1 }, waitFor, time.Millisecond)

	srv.Send([]byte("END\r\n"))

	pump(t, pg, poller, func() bool { return srv.Accepted() == 2 })
	assert.Zero(t, h.failures)

	req, ch := getReq("k")
	require.True(t, pg.Enqueue(req))
	var r result
	pump(t, pg, poller, received(ch, &r))
	require.NoError(t, r.err)
	assert.Equal(t, "END\r\n", string(r.resp))
}

func TestPollGroup_ReconnectFailureCallsFailureHook(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetMode(upstreamtest.ModeSilent)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, poller, h := newTestPollGroup(t, ctx, srv.Addr())

	req, ch := getReq("k")
	require.True(t, pg.Enqueue(req))
	pump(t, pg, poller, func() bool { return srv.Requests() == 1 })

	srv.Stop() // connection reset, reconnect refused

	var r result
	pump(t, pg, poller, func() bool { return h.failures > 0 })
	require.True(t, received(ch, &r)())
	require.Error(t, r.err)
	assert.Equal(t, 1, h.failures)
}

func TestPollGroup_RecreateUnknownSocketIsNoop(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, poller, h := newTestPollGroup(t, ctx, srv.Addr())

	// a socket that does not belong to the group
	other, otherPoller, _ := newTestPollGroup(t, ctx, srv.Addr())
	sock := other.sockets[0]
	other.sockets = nil
	otherPoller.Delete(sock.FD())

	pg.recreateSocket(sock, ErrClosed)

	assert.Equal(t, 1, pg.NumSockets())
	assert.Zero(t, h.failures)
	pump(t, pg, poller, func() bool { return true })
}

func TestPollGroup_DispatchFailsWhenSocketIsNotInPoller(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, poller, h := newTestPollGroup(t, ctx, srv.Addr())

	poller.Delete(pg.sockets[0].FD())
	pg.dispatch()

	assert.Equal(t, 1, h.failures)
	require.Len(t, h.pollerFailures, 1)
	require.ErrorIs(t, h.pollerFailures[0], unix.ENOENT)
}

func TestGetSocketError(t *testing.T) {
	t.Run("not a socket", func(t *testing.T) {
		var fds [2]int
		require.NoError(t, unix.Pipe(fds[:]))
		t.Cleanup(func() {
			_ = unix.Close(fds[0])
			_ = unix.Close(fds[1])
		})
		require.ErrorIs(t, getSocketError(fds[0]), unix.ENOTSOCK)
	})

	t.Run("healthy socket", func(t *testing.T) {
		srv := upstreamtest.NewServer(t)
		ctx := upstreamtest.NewAppContext(t, oneConn)
		pg, _, _ := newTestPollGroup(t, ctx, srv.Addr())
		require.NoError(t, getSocketError(pg.sockets[0].FD()))
	})

	t.Run("pending error", func(t *testing.T) {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = unix.Close(fd) })
		require.NoError(t, unix.SetNonblock(fd, true))

		addr := upstreamtest.RefusedAddr(t)
		_, port, _ := strings.Cut(addr, ":")
		var p int
		_, err = fmt.Sscan(port, &p)
		require.NoError(t, err)

		err = unix.Connect(fd, &unix.SockaddrInet4{Port: p, Addr: [4]byte{127, 0, 0, 1}})
		if err != nil {
			require.ErrorIs(t, err, unix.EINPROGRESS)
			pfds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}} // #nosec G115
			_, err = unix.Poll(pfds, int(waitFor.Milliseconds()))
			require.NoError(t, err)
			require.ErrorIs(t, getSocketError(fd), unix.ECONNREFUSED)
		}
	})
}

func TestPollGroup_ReadFailureRecreatesSocket(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetMode(upstreamtest.ModeSilent)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, _, h := newTestPollGroup(t, ctx, srv.Addr())
	sock := pg.sockets[0]

	req, ch := getReq("k")
	require.True(t, pg.Enqueue(req))
	pg.dispatchOnSocket(sock)
	require.NoError(t, sock.Flush())
	require.Eventually(t, func() bool { return srv.Requests() == 1 }, waitFor, time.Millisecond)

	// wait for the FIN, then drive the read by hand (with the poller, macOS
	// reports the hangup through the error callback instead)
	srv.DropConns()
	pfds := []unix.PollFd{{Fd: int32(sock.FD()), Events: unix.POLLIN}} // #nosec G115
	_, err := unix.Poll(pfds, int(waitFor.Milliseconds()))
	require.NoError(t, err)
	pg.onReadable(sock)

	r := await(t, ch)
	require.ErrorIs(t, r.err, unix.ECONNRESET)
	require.Len(t, pg.sockets, 1)
	assert.NotSame(t, sock, pg.sockets[0])
	assert.Zero(t, h.failures)
}

func TestPollGroup_InflightLimitPerSocket(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetMode(upstreamtest.ModeSilent)
	ctx := upstreamtest.NewAppContext(t, oneConn)
	pg, _, _ := newTestPollGroup(t, ctx, srv.Addr())
	sock := pg.sockets[0]

	// fill the socket's inflight ring without flushing anything
	for range DefaultRingSize {
		req, _ := getReq("k")
		require.True(t, pg.Enqueue(req))
	}
	for range DefaultRingSize / SocketIOBatch {
		pg.dispatchOnSocket(sock)
	}
	require.False(t, sock.CanPushToInflight())

	req, _ := getReq("k")
	require.True(t, pg.Enqueue(req))
	pg.dispatchOnSocket(sock)

	got, ok := pg.requestRing.Peek()
	require.True(t, ok, "the request must wait in the ring")
	assert.Same(t, req, got)
}

func TestPollGroup_WriteBufferFullKeepsRequestQueued(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.SetMode(upstreamtest.ModeSilent)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) {
		c.UpstreamConnections = 1
		c.BufferSize = 16
	})
	pg, poller, _ := newTestPollGroup(t, ctx, srv.Addr())
	sock := pg.sockets[0]

	first, firstCh := getReq("0123456") // 13 bytes
	second, _ := getReq("abc")          // 9 bytes: does not fit before a flush
	require.True(t, pg.Enqueue(first))
	require.True(t, pg.Enqueue(second))

	pg.dispatchOnSocket(sock)
	got, ok := pg.requestRing.Peek()
	require.True(t, ok)
	assert.Same(t, second, got)

	// once the buffer is flushed the queued request goes out
	pump(t, pg, poller, func() bool { return srv.Requests() == 2 })
	requireNoMore(t, firstCh)
}

func TestPollGroup_RequestLargerThanBufferIsRejected(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) {
		c.UpstreamConnections = 1
		c.BufferSize = 1024
	})
	pg, poller, _ := newTestPollGroup(t, ctx, srv.Addr())

	big := strings.Repeat("x", 2000)
	req, ch := newReq(ascii.SetCmd, "set k 0 0 2000\r\n"+big+"\r\n")
	require.True(t, pg.Enqueue(req))

	var r result
	pump(t, pg, poller, received(ch, &r))
	require.ErrorIs(t, r.err, ErrRequestTooLarge)

	// the oversized request must not block the requests queued behind it
	req, ch = newReq(ascii.VersionCmd, "version\r\n")
	require.True(t, pg.Enqueue(req))
	pump(t, pg, poller, received(ch, &r))
	require.NoError(t, r.err)
	assert.Equal(t, "VERSION 1.6.0-fake\r\n", string(r.resp))
}

func TestPollGroup_RunOfOversizedRequestsDoesNotStallTheQueue(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) {
		c.UpstreamConnections = 1
		c.BufferSize = 1024
	})
	pg, _, _ := newTestPollGroup(t, ctx, srv.Addr())

	// more rejected requests than one dispatch batch, then a valid one
	big := "set k 0 0 2000\r\n" + strings.Repeat("x", 2000) + "\r\n"
	rejected := 0
	for range 2*SocketIOBatch + 1 {
		req, _ := newReq(ascii.SetCmd, big)
		req.Callback = func(_ *types.ByteBuf, err error) {
			if errors.Is(err, ErrRequestTooLarge) {
				rejected++
			}
		}
		require.True(t, pg.Enqueue(req))
	}
	req, _ := newReq(ascii.VersionCmd, "version\r\n")
	require.True(t, pg.Enqueue(req))

	// a single wakeup: one dispatch must get to the valid request
	pg.dispatch()
	assert.Equal(t, 2*SocketIOBatch+1, rejected)
	_, queued := pg.requestRing.Peek()
	assert.False(t, queued, "the valid request is still queued")
}

func TestPollGroup_ResponseLargerThanBufferFailsSocket(t *testing.T) {
	srv := upstreamtest.NewServer(t)
	srv.Set("big", []byte(strings.Repeat("x", 2000)))
	ctx := upstreamtest.NewAppContext(t, func(c *config.App) {
		c.UpstreamConnections = 1
		c.BufferSize = 1024
	})
	pg, poller, h := newTestPollGroup(t, ctx, srv.Addr())

	req, ch := getReq("big")
	require.True(t, pg.Enqueue(req))

	var r result
	pump(t, pg, poller, received(ch, &r))
	require.ErrorIs(t, r.err, ErrResponseTooLarge)
	assert.Zero(t, h.failures)

	// the connection was replaced, so the stream is usable again
	req, ch = newReq(ascii.VersionCmd, "version\r\n")
	require.True(t, pg.Enqueue(req))
	pump(t, pg, poller, received(ch, &r))
	require.NoError(t, r.err)
	assert.Equal(t, "VERSION 1.6.0-fake\r\n", string(r.resp))
}
