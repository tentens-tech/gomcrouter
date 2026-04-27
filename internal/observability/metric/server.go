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

func NewServer(ctx context.Context, listen string) *Server {
	return &Server{
		ctx:    ctx,
		listen: listen,
	}
}

func (s *Server) Listen() {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	s.server = &http.Server{
		Addr:              s.listen,
		Handler:           mux,
		ReadHeaderTimeout: metricsReadHeaderTimeout,
		WriteTimeout:      metricsWriteTimeout,
		IdleTimeout:       metricsIdleTimeout,
	}

	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (s *Server) Stop(ctx context.Context) {
	_ = s.server.Shutdown(ctx)
}
