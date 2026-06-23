package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/assanoff/servicekit/logger"

	"github.com/assanoff/service-kit-x/internal/app/config"
)

// restServer adapts an *http.Server to worker.Runnable so the supervisor can
// start and gracefully stop it alongside other runnables.
type restServer struct {
	log    *logger.Logger
	server *http.Server
}

func newRestServer(cfg config.HTTP, log *logger.Logger, handler http.Handler) *restServer {
	return &restServer{
		log: log,
		server: &http.Server{
			Addr:              cfg.Addr,
			Handler:           handler,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		},
	}
}

func (s *restServer) Name() string { return "rest-server" }

func (s *restServer) Start(ctx context.Context) error {
	s.log.Info(ctx, "rest server listening", "addr", s.server.Addr)
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *restServer) Stop(ctx context.Context) error {
	shutdownCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	s.log.Info(shutdownCtx, "rest server shutting down")
	return s.server.Shutdown(shutdownCtx)
}
