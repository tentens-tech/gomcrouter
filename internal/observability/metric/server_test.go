package metric

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freeAddr returns a 127.0.0.1 address with a port that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// startServer runs s.Listen in the background and returns a channel closed
// when Listen returns.
func startServer(s *Server) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Listen()
	}()
	return done
}

func waitClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("metrics server did not stop")
	}
}

func get(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)
	return http.DefaultClient.Do(req)
}

func TestNewServer_ConfiguresTimeouts(t *testing.T) {
	s := NewServer(t.Context(), "127.0.0.1:0")

	require.NotNil(t, s.server)
	assert.Equal(t, "127.0.0.1:0", s.server.Addr)
	assert.Equal(t, metricsReadHeaderTimeout, s.server.ReadHeaderTimeout)
	assert.Equal(t, metricsWriteTimeout, s.server.WriteTimeout)
	assert.Equal(t, metricsIdleTimeout, s.server.IdleTimeout)
}

func TestServer_ServesMetricsAndShutsDown(t *testing.T) {
	addr := freeAddr(t)
	s := NewServer(t.Context(), addr)
	done := startServer(s)
	t.Cleanup(func() {
		s.Stop(context.Background())
		waitClosed(t, done)
	})

	var body string
	require.Eventually(t, func() bool {
		resp, err := get(t, "http://"+addr+"/metrics")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			return false
		}
		body = string(b)
		return true
	}, 5*time.Second, 10*time.Millisecond)

	// the global Collector is registered on the default registry served here
	assert.Contains(t, body, Namespace+"_"+VersionMetric)

	resp, err := get(t, "http://"+addr+"/unknown")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	s.Stop(context.Background())
	waitClosed(t, done)

	resp, err = get(t, "http://"+addr+"/metrics")
	if err == nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err, "server must not accept requests after Stop")
}

func TestServer_StopBeforeListen(t *testing.T) {
	s := NewServer(t.Context(), freeAddr(t))

	// Stop may race with (or precede) Listen, which runs in its own
	// goroutine. It must not panic, and a later Listen must return at once.
	require.NotPanics(t, func() { s.Stop(context.Background()) })

	waitClosed(t, startServer(s))
}
