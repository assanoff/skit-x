package cmd

import (
	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/server"
)

// ConsumerCommand runs the queue consumers only (widget-audit), plus the
// technical routes on DEBUG_ADDR. No API, no background workers. Competing
// consumers, so more than one replica is safe.
type ConsumerCommand struct {
	config.ServerOpts
}

// Execute implements flags.Commander.
func (c *ConsumerCommand) Execute(_ []string) error {
	return run(c.ServerOpts,
		server.WithServer(c.Debug.Addr, server.Debug),
		server.WithConsumers(),
	)
}
