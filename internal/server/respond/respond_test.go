package respond

import (
	"errors"
	"testing"

	"github.com/panjf2000/gnet/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/machinery/pool"
	"github.com/tentens-tech/gomcrouter/internal/machinery/time"
	"github.com/tentens-tech/gomcrouter/internal/proto/ascii"
	"github.com/tentens-tech/gomcrouter/internal/types"
)

// fakeConn is a minimal gnet.Conn: only AsyncWrite is implemented, any other
// method call panics on the nil embedded interface. Callbacks are queued and
// run by flush, mimicking gnet invoking them later from the event loop.
type fakeConn struct {
	gnet.Conn

	written   [][]byte
	callbacks []gnet.AsyncCallback
	err       error
	// closedAfter is the number of writes queued before Close, -1 if open
	closedAfter int
}

func newFakeConn() *fakeConn { return &fakeConn{closedAfter: -1} }

func (f *fakeConn) Close() error {
	f.closedAfter = len(f.written)
	return nil
}

func (f *fakeConn) AsyncWrite(buf []byte, cb gnet.AsyncCallback) error {
	if f.err != nil {
		return f.err
	}
	f.written = append(f.written, append([]byte(nil), buf...))
	f.callbacks = append(f.callbacks, cb)
	return nil
}

func (f *fakeConn) flush(t *testing.T) {
	t.Helper()
	for _, cb := range f.callbacks {
		require.NoError(t, cb(f, nil))
	}
	f.callbacks = nil
}

func TestAsyncRespond_NilResponseIsNoop(t *testing.T) {
	conn := &fakeConn{}
	req := &types.Request{Conn: conn}

	AsyncRespond(nil, req)

	assert.Empty(t, conn.written)
	assert.Empty(t, conn.callbacks)
}

func TestAsyncRespond_WritesAndReleasesPooledBuffer(t *testing.T) {
	conn := &fakeConn{}
	req := &types.Request{Conn: conn, Cmd: ascii.SetCmd, Ts: time.NanoTime()}

	resp := pool.BufferPool.Get(len("STORED\r\n"))
	copy(resp.B, "STORED\r\n")

	AsyncRespond(resp, req)

	require.Len(t, conn.written, 1)
	assert.Equal(t, "STORED\r\n", string(conn.written[0]))
	// buffer must stay untouched until gnet has finished writing it
	assert.Equal(t, "STORED\r\n", string(resp.B))

	conn.flush(t)

	// BufferPool.Put reslices pooled buffers to their full size class,
	// which proves the buffer was returned to the pool in the callback.
	assert.Len(t, resp.B, pool.XSmallBufferSize)
}

func TestAsyncRespond_StaticBufferIsNotMutated(t *testing.T) {
	conn := &fakeConn{}
	req := &types.Request{Conn: conn}

	backing := []byte("SERVER_ERROR static\r\nextra")
	static := &types.ByteBuf{B: backing[:21]}

	AsyncRespond(static, req)
	conn.flush(t)
	AsyncRespond(static, req)
	conn.flush(t)

	require.Len(t, conn.written, 2)
	for _, w := range conn.written {
		assert.Equal(t, "SERVER_ERROR static\r\n", string(w))
	}
	assert.Equal(t, "SERVER_ERROR static\r\n", string(static.B))
}

func TestAsyncRespond_WriteErrorPanics(t *testing.T) {
	writeErr := errors.New("poller is closed")
	conn := &fakeConn{err: writeErr}
	req := &types.Request{Conn: conn}

	defer func() {
		r := recover()
		require.NotNil(t, r, "AsyncRespond must fail fast on a synchronous AsyncWrite error")
		err, ok := r.(error)
		require.True(t, ok, "panic value must be an error, got %T", r)
		require.ErrorIs(t, err, writeErr)
		assert.Contains(t, err.Error(), "server async write fail")
	}()

	AsyncRespond(&types.ByteBuf{B: []byte("END\r\n")}, req)
}

func static(s string) *types.ByteBuf { return &types.ByteBuf{B: []byte(s)} }

func writtenStrings(f *fakeConn) []string {
	out := make([]string, 0, len(f.written))
	for _, w := range f.written {
		out = append(out, string(w))
	}
	return out
}

func TestAsyncRespond_KeepsRequestOrder(t *testing.T) {
	conn := newFakeConn()
	st := &types.ConnState{}
	reqs := make([]*types.Request, 4)
	for i := range reqs {
		reqs[i] = &types.Request{Conn: conn, State: st, Seq: st.NextSeq}
		st.NextSeq++
	}

	// replies complete in the order 2, 3, 0, 1
	AsyncRespond(static("r2\r\n"), reqs[2])
	AsyncRespond(static("r3\r\n"), reqs[3])
	assert.Empty(t, conn.written, "replies wait for the reply to the first request")

	AsyncRespond(static("r0\r\n"), reqs[0])
	assert.Equal(t, []string{"r0\r\n"}, writtenStrings(conn))

	AsyncRespond(static("r1\r\n"), reqs[1])
	assert.Equal(t, []string{"r0\r\n", "r1\r\n", "r2\r\n", "r3\r\n"}, writtenStrings(conn))
	assert.Empty(t, st.Pending)
	assert.Equal(t, uint64(4), st.Written)
	conn.flush(t)
}

func TestAsyncRespond_NilReplyReleasesItsTurn(t *testing.T) {
	conn := newFakeConn()
	st := &types.ConnState{NextSeq: 2}
	first := &types.Request{Conn: conn, State: st, Seq: 0}
	second := &types.Request{Conn: conn, State: st, Seq: 1}

	AsyncRespond(static("second\r\n"), second)
	AsyncRespond(nil, first)

	assert.Equal(t, []string{"second\r\n"}, writtenStrings(conn))
	assert.Equal(t, uint64(2), st.Written)
}

// closeAtOnce makes the ordered close call the fake connection's Close: the
// fake has no event loop to wait on.
func closeAtOnce(t *testing.T) {
	t.Helper()
	orig := closeConn
	closeConn = func(c gnet.Conn, _ *types.ConnState, _ int64) { _ = c.Close() }
	t.Cleanup(func() { closeConn = orig })
}

func TestAsyncRespondAndClose_AfterEarlierReplies(t *testing.T) {
	closeAtOnce(t)
	conn := newFakeConn()
	st := &types.ConnState{}
	req := &types.Request{Conn: conn, State: st, Seq: st.NextSeq}
	st.NextSeq++

	AsyncRespondAndClose(conn, st, static("ERROR\r\n"))
	assert.True(t, st.Quit)
	assert.Equal(t, uint64(2), st.NextSeq)
	assert.Empty(t, conn.written)
	assert.Equal(t, -1, conn.closedAfter, "the connection closes after the reply to the earlier request")

	AsyncRespond(static("STORED\r\n"), req)
	assert.Equal(t, []string{"STORED\r\n", "ERROR\r\n"}, writtenStrings(conn))
	assert.Equal(t, 2, conn.closedAfter)
	conn.flush(t)
}

func TestAsyncRespondAndClose_WithoutReply(t *testing.T) {
	closeAtOnce(t)
	conn := newFakeConn()

	AsyncRespondAndClose(conn, &types.ConnState{}, nil)
	assert.Empty(t, conn.written)
	assert.Equal(t, 0, conn.closedAfter)

	// without a state the connection closes at once
	conn = newFakeConn()
	AsyncRespondAndClose(conn, nil, static("bye\r\n"))
	assert.Equal(t, []string{"bye\r\n"}, writtenStrings(conn))
	assert.Equal(t, 1, conn.closedAfter)
	conn.flush(t)
}
