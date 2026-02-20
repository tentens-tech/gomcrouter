package metric

import (
	"context"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log"
	"net/http"
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
		Addr:    s.listen,
		Handler: mux,
	}

	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func (s *Server) Stop(ctx context.Context) {
	_ = s.server.Shutdown(ctx)
}
