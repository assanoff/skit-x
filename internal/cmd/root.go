// Package cmd is the command-line entrypoint. Configuration and subcommands are
// declared with go-flags tags (see app/config); each subcommand implements
// flags.Commander, so go-flags parses flags+env and dispatches Execute.
package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	skconfig "github.com/assanoff/skit/config"
	"github.com/assanoff/skit/logger"
	skotel "github.com/assanoff/skit/otel"

	"github.com/assanoff/skit-x/internal/app/config"
)

// Opts declares the available subcommands.
//
// The four long-running commands run the same binary with a different list of
// options (server.WithServer / WithGRPC / WithGateway / WithWorkers /
// WithConsumers, see internal/app/server/options.go): serve = everything in one
// process, the others split it across pods. Each of them lists a server with
// server.Debug on DEBUG_ADDR, so probes work whatever the process runs.
type Opts struct {
	Serve    ServeCommand    `command:"serve" description:"run everything in one process: transports + workers + consumers"`
	HTTP     HTTPCommand     `command:"http" description:"run the transports only (REST/gRPC/gateway), scale to N replicas"`
	Worker   WorkerCommand   `command:"worker" description:"run the background workers only (single replica)"`
	Consumer ConsumerCommand `command:"consumer" description:"run the queue consumers only"`
	Migrate  MigrateCommand  `command:"migrate" description:"apply database migrations (up|down|status)"`
	Count    CountCommand    `command:"count" description:"print the number of widgets (DB-only command demo)"`
	Version  VersionCommand  `command:"version" description:"print the build version"`
}

// Execute loads .env, parses flags/env, runs the selected subcommand and returns
// the process exit code.
//
// The .env file is optional: every option has a default and configuration also
// comes from the process environment (in production, from the orchestrator). A
// missing file is not an error — startup proceeds on defaults — but we warn so
// the absence is visible. The app logger is not built yet here (it needs the
// parsed config), so the warning goes through slog's default handler.
func Execute() int {
	var opts Opts

	const envFile = ".env"
	if _, err := os.Stat(envFile); errors.Is(err, os.ErrNotExist) {
		slog.Warn("no .env file found; starting on defaults and process environment", "file", envFile)
	}

	if err := skconfig.Parse(&opts, envFile); err != nil {
		if skconfig.IsHelp(err) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
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
