// Package upstreamtest holds test helpers shared by the internal/upstream/...
// test suites: an in-process fake memcached server speaking enough of the
// ASCII protocol and a ready-to-use *config.AppContext.
//
// It is a regular (non _test.go) package only because Go cannot share
// _test.go files between packages; being under internal/upstream/internal it
// is importable solely from internal/upstream/... and is never linked into
// the production binary (only test binaries import it).
package upstreamtest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"go.uber.org/zap"
)

// Mode selects how the fake server answers requests.
type Mode int32

const (
	// ModeNormal answers every request.
	ModeNormal Mode = iota
	// ModeSilent reads requests but never answers them.
	ModeSilent
	// ModeClose closes the connection as soon as a request line was read.
	ModeClose
	// ModeMalformed answers every request with a broken VALUE block.
	ModeMalformed
)

// CAS is the cas unique the fake server reports for every gets hit. It is
// fixed so tests can spell out the expected response.
const CAS = 1

// MaxBodyLen bounds the data block of a storage command. The fake server
// closes the connection on larger (or negative) lengths instead of
// allocating the buffer.
const MaxBodyLen = 16 << 20

// MalformedResponse is a VALUE block followed by garbage instead of END.
var MalformedResponse = []byte("VALUE k 0 1\r\nx\r\nGARBAGE\r\n")

// Server is a fake memcached listening on 127.0.0.1.
type Server struct {
	tb   testing.TB
	addr string

	mode     atomic.Int32
	delay    atomic.Int64
	accepted atomic.Int64
	requests atomic.Int64

	mu    sync.Mutex
	ln    net.Listener
	conns map[net.Conn]struct{}
	data  map[string][]byte
	done  chan struct{}
	wg    sync.WaitGroup
}

// NewServer starts a fake memcached on a random loopback port. It is stopped
// automatically when the test ends.
func NewServer(tb testing.TB) *Server {
	tb.Helper()

	s := &Server{
		tb:    tb,
		conns: make(map[net.Conn]struct{}),
		data:  make(map[string][]byte),
	}
	s.listen("127.0.0.1:0")
	tb.Cleanup(s.Stop)

	return s
}

func (s *Server) listen(addr string) {
	s.tb.Helper()

	ln, err := net.Listen("tcp", addr)
	require.NoError(s.tb, err)

	s.mu.Lock()
	s.ln = ln
	s.addr = ln.Addr().String()
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()

	s.wg.Add(1)
	go s.acceptLoop(ln, done)
}

// Addr returns host:port of the server.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// SetMode changes how subsequent requests are answered.
func (s *Server) SetMode(m Mode) { s.mode.Store(int32(m)) }

// SetDelay delays every response by d.
func (s *Server) SetDelay(d time.Duration) { s.delay.Store(int64(d)) }

// Accepted is the total number of accepted connections.
func (s *Server) Accepted() int { return int(s.accepted.Load()) }

// Requests is the total number of request lines read.
func (s *Server) Requests() int { return int(s.requests.Load()) }

// ActiveConns is the number of currently open connections.
func (s *Server) ActiveConns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Set stores a value as if a client sent "set".
func (s *Server) Set(key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte(nil), value...)
}

// Send writes raw bytes to every open connection (unsolicited data).
func (s *Server) Send(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		_, _ = c.Write(b)
	}
}

// DropConns closes all open connections but keeps listening.
func (s *Server) DropConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		_ = c.Close()
	}
}

// Stop closes the listener and all connections and waits for every server
// goroutine to exit. It is idempotent.
func (s *Server) Stop() {
	s.mu.Lock()
	if s.ln == nil {
		s.mu.Unlock()
		return
	}
	_ = s.ln.Close()
	s.ln = nil
	close(s.done)
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()

	s.wg.Wait()
}

// Restart listens again on the same address after Stop.
func (s *Server) Restart() {
	s.tb.Helper()
	s.listen(s.Addr())
}

func (s *Server) acceptLoop(ln net.Listener, done chan struct{}) {
	defer s.wg.Done()

	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}

		s.mu.Lock()
		select {
		case <-done:
			s.mu.Unlock()
			_ = c.Close()
			return
		default:
		}
		s.conns[c] = struct{}{}
		s.accepted.Add(1)
		s.wg.Add(1)
		s.mu.Unlock()

		go s.serve(c, done)
	}
}

func (s *Server) serve(c net.Conn, done chan struct{}) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = c.Close()
	}()

	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		s.requests.Add(1)

		resp, err := s.handle(bytes.TrimRight(line, "\r\n"), r)
		if err != nil {
			return
		}

		switch Mode(s.mode.Load()) {
		case ModeSilent:
			continue
		case ModeClose:
			return
		case ModeMalformed:
			resp = MalformedResponse
		case ModeNormal:
		}

		if d := time.Duration(s.delay.Load()); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-done:
				t.Stop()
				return
			case <-t.C:
			}
		}

		if _, err = c.Write(resp); err != nil {
			return
		}
	}
}

var (
	errQuit    = errors.New("quit")
	errBodyLen = errors.New("data block length out of range")
)

func (s *Server) handle(line []byte, r *bufio.Reader) ([]byte, error) {
	fields := bytes.Fields(line)
	if len(fields) == 0 {
		return []byte("ERROR\r\n"), nil
	}

	switch string(fields[0]) {
	case "get", "gets":
		withCAS := string(fields[0]) == "gets"
		var out bytes.Buffer
		s.mu.Lock()
		for _, k := range fields[1:] {
			v, ok := s.data[string(k)]
			if !ok {
				continue
			}
			if withCAS {
				_, _ = fmt.Fprintf(&out, "VALUE %s 0 %d %d\r\n%s\r\n", k, len(v), CAS, v)
			} else {
				_, _ = fmt.Fprintf(&out, "VALUE %s 0 %d\r\n%s\r\n", k, len(v), v)
			}
		}
		s.mu.Unlock()
		out.WriteString("END\r\n")
		return out.Bytes(), nil
	case "set", "add", "replace", "append", "prepend":
		if len(fields) < 5 {
			return []byte("CLIENT_ERROR bad command line format\r\n"), nil
		}
		n, convErr := strconv.Atoi(string(fields[4]))
		if convErr != nil {
			return []byte("CLIENT_ERROR bad data chunk\r\n"), nil //nolint:nilerr // protocol error answered to the client
		}
		if n < 0 || n > MaxBodyLen {
			return nil, errBodyLen
		}
		body := make([]byte, n+2)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		s.Set(string(fields[1]), body[:n])
		return []byte("STORED\r\n"), nil
	case "delete":
		if len(fields) < 2 {
			return []byte("ERROR\r\n"), nil
		}
		s.mu.Lock()
		_, ok := s.data[string(fields[1])]
		delete(s.data, string(fields[1]))
		s.mu.Unlock()
		if !ok {
			return []byte("NOT_FOUND\r\n"), nil
		}
		return []byte("DELETED\r\n"), nil
	case "version":
		return []byte("VERSION 1.6.0-fake\r\n"), nil
	case "quit":
		return nil, errQuit
	default:
		return []byte("ERROR\r\n"), nil
	}
}

// RefusedAddr returns a loopback address on which nothing is listening.
func RefusedAddr(tb testing.TB) string {
	tb.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err)
	addr := ln.Addr().String()
	require.NoError(tb, ln.Close())

	return addr
}

// Options tweak the AppContext built by NewAppContext.
type Options func(*config.App)

// NewAppContext returns an AppContext with small, test friendly timeouts. Its
// context is cancelled when the test ends.
func NewAppContext(tb testing.TB, opts ...Options) *config.AppContext {
	tb.Helper()

	cfg := &config.App{
		LogLevel:            "debug",
		UpstreamConnections: 2,
		TimeoutMillis:       200,
		HealthCheckInterval: time.Hour,
		DnsCacheTTL:         time.Hour,
		BufferSize:          64 * 1024,
	}
	for _, o := range opts {
		o(cfg)
	}
	cfg.TimeoutNs = cfg.TimeoutMillis * int64(time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	tb.Cleanup(cancel)

	return &config.AppContext{
		Context: ctx,
		Logger:  zap.NewNop().Sugar(),
		Config:  cfg,
	}
}
