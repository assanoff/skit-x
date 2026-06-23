package cmd

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/assanoff/servicekit/closer"

	"github.com/assanoff/service-kit-x/internal/app/config"
	"github.com/assanoff/service-kit-x/internal/app/server"
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Release resources (DB, tracer, ...) in LIFO order even on startup failure.
	defer func() { _ = closer.CloseSync() }()

	log.Info(ctx, "starting service",
		"env", cfg.Env, "http_enabled", !cfg.HTTP.Disabled, "http", cfg.HTTP.Addr,
		"grpc_enabled", !cfg.GRPC.Disabled, "grpc", cfg.GRPC.Addr)

	app, err := server.New(ctx, cfg, log)
	if err != nil {
		return err
	}

	if err := app.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}

	log.Info(context.Background(), "service stopped")
	return nil
}
