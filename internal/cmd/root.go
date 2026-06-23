// Package cmd is the command-line entrypoint. Configuration and subcommands are
// declared with go-flags tags (see app/config); each subcommand implements
// flags.Commander, so go-flags parses flags+env and dispatches Execute.
package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	skconfig "github.com/assanoff/servicekit/config"
	"github.com/assanoff/servicekit/logger"
	skotel "github.com/assanoff/servicekit/otel"

	"github.com/assanoff/service-kit-x/internal/app/config"
)

// Opts declares the available subcommands.
type Opts struct {
	Serve   ServeCommand   `command:"serve" description:"run the REST and/or gRPC server"`
	Migrate MigrateCommand `command:"migrate" description:"apply database migrations (up|down|status)"`
	Version VersionCommand `command:"version" description:"print the build version"`
}

// Execute loads .env, parses flags/env, and runs the selected subcommand.
func Execute() {
	var opts Opts
	if err := skconfig.Parse(&opts, ".env"); err != nil {
		if skconfig.IsHelp(err) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func buildLogger(cfg config.ServerOpts) *logger.Logger {
	log := logger.New(os.Stdout, logger.Config{
		Service:   cfg.Service,
		Level:     parseLevel(cfg.LogLevel),
		AddSource: true,
		TraceIDFn: skotel.GetTraceID,
	})
	// Route the global slog (used by dim/closer for init/shutdown logs) through
	// the same handler.
	slog.SetDefault(log.Slog())
	return log
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
