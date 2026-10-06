package cmd

import (
	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/server"
)

// WorkerCommand runs the background work only: the widget-import loop, the
// widget-count poller and the transactional-outbox workers, plus the technical
// routes on DEBUG_ADDR. No API, no consumers.
//
// Run it as a single replica: these loops poll the same rows, so extra replicas
// only add database load (and any wall-clock job would fire twice).
type WorkerCommand struct {
	config.ServerOpts
}

// Execute implements flags.Commander.
func (c *WorkerCommand) Execute(_ []string) error {
	return run(c.ServerOpts,
		server.WithServer(c.Debug.Addr, server.Debug),
		server.WithWorkers(),
	)
}
