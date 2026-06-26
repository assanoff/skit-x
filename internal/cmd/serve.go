package cmd

import (
	"context"

	"github.com/assanoff/skit/app"

	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/server"
)

// ServeCommand runs the long-lived server. It embeds ServerOpts so go-flags
// fills the configuration directly from flags and environment.
type ServeCommand struct {
	config.ServerOpts
}

// Execute implements flags.Commander.
func (c *ServeCommand) Execute(_ []string) error {
	cfg := c.ServerOpts
	log := buildLogger(cfg)
	ctx := context.Background()

	log.Info(ctx, "starting service",
		"env", cfg.Env, "http", cfg.HTTP.Addr, "grpc", cfg.GRPC.Addr, "gateway", cfg.Gateway.Addr)

	a, err := server.New(ctx, cfg, log)
	if err != nil {
		return err
	}

	// app.Run owns the signal context, the worker.Group supervision, and the
	// global closer (LIFO resource release) — see internal/app/deps + provider.
	return app.Run(ctx, app.RunConfig{
		Logger:          log.Slog(),
		ShutdownTimeout: cfg.HTTP.ShutdownTimeout,
	}, a.Runnables()...)
}
