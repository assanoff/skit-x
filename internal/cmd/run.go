package cmd

import (
	"context"

	"github.com/assanoff/skit/app"

	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/server"
)

// run is the body shared by every long-running command (serve, http, worker,
// consumer). The only difference between them is the options they list —
// server.WithServer, server.WithGRPC, server.WithGateway, server.WithWorkers,
// server.WithConsumers (see internal/app/server/options.go): server.New builds
// the process out of them, app.Run supervises it.
//
// Every one of them lists a server with server.Debug — /healthz, /readyz,
// /metrics, /debug/pprof/, /debug/components — which is what keeps the pod
// probeable and alive until SIGTERM, whatever else it runs.
func run(cfg config.ServerOpts, options ...server.Option) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	log := buildLogger(cfg)
	ctx := context.Background()

	a, err := server.New(ctx, cfg, log, options...)
	if err != nil {
		return err
	}

	// What this process runs, as its command composed it. GET /debug/components
	// serves the same values.
	c := a.Components()
	log.InfoContext(ctx, "starting service", "env", cfg.Env,
		"servers", c.Servers, "workers", c.Workers, "consumers", c.Consumers)

	// app.Run owns the signal context, the worker.Group supervision, and the
	// global closer (LIFO resource release) — see internal/app/deps + provider.
	return app.Run(ctx, app.RunConfig{
		Logger:          log,
		ShutdownTimeout: cfg.HTTP.ShutdownTimeout,
	}, a.Runnables()...)
}
