package cmd

import (
	"context"

	"github.com/assanoff/skit/app"

	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/deps"
)

// CountCommand prints the number of widgets. It demonstrates a one-shot CLI
// command bootstrapped with app.RunCommand: it assembles only the dependencies
// it needs (the DB, via deps.Storage), runs a business read, and lets the closer
// release the connection afterward — no servers, no broker, no handlers.
type CountCommand struct {
	config.ServerOpts
}

// Execute implements flags.Commander.
func (c *CountCommand) Execute(_ []string) error {
	cfg := c.ServerOpts
	log := buildLogger(cfg)
	d := &deps.Deps{Opts: cfg, Logger: log}

	return app.RunCommand(context.Background(), app.CommandConfig{}, d, deps.Storage,
		func(ctx context.Context, d *deps.Deps) error {
			var n int
			if err := d.DB(ctx).GetContext(ctx, &n, `SELECT count(*) FROM widgets`); err != nil {
				return err
			}
			log.Info(ctx, "widget count", "count", n)
			return nil
		})
}
