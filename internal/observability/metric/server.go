package metric

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HTTP timeouts for the metrics server. ReadHeaderTimeout is the one that
// closes Slowloris-style attacks (gosec G112); the others bound how long a
// single Prometheus scrape can occupy a goroutine.
const (
	metricsReadHeaderTimeout = 5 * time.Second
	metricsWriteTimeout      = 30 * time.Second
	metricsIdleTimeout       = 60 * time.Second
)

type Server struct {
	ctx    context.Context
	listen string

	server *http.Server
}

// NewServer builds the metrics HTTP server. The http.Server is created here,
// not in Listen, so that Stop (called from another goroutine) never races
// with Listen nor dereferences a nil server when it runs first.
func NewServer(ctx context.Context, listen string) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	return &Server{
		ctx:    ctx,
		listen: listen,
		server: &http.Server{
			Addr:              listen,
			Handler:           mux,
			ReadHeaderTimeout: metricsReadHeaderTimeout,
			WriteTimeout:      metricsWriteTimeout,
			IdleTimeout:       metricsIdleTimeout,
		},
	}
}

// Listen serves /metrics until Stop is called. It returns immediately if
// Stop was already called.
func (s *Server) Listen() {
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (s *Server) Stop(ctx context.Context) {
	_ = s.server.Shutdown(ctx)
}
