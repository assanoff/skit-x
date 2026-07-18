package deps

import (
	"context"
	"fmt"

	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/provider"
	"github.com/assanoff/skit/queue"
)

// initStore initializes the Postgres connection pool and assigns it to the
// container, using the SDK's ready-made Postgres provider.
var initStore = func(c *Deps) (cleanup dim.CleanupFunc, err error) {
	c.DB, cleanup = dim.NewResource("Store", provider.Postgres(dbx.Config{
		User:         c.Opts.DB.User,
		Password:     c.Opts.DB.Password,
		Host:         c.Opts.DB.Host,
		Name:         c.Opts.DB.Name,
		Schema:       c.Opts.DB.Schema,
		MaxIdleConns: c.Opts.DB.MaxIdleConns,
		MaxOpenConns: c.Opts.DB.MaxOpenConns,
		DisableTLS:   !c.Opts.DB.TLS,
	}))
	return cleanup, nil
}

// initQueue builds the Postgres-backed work queue. The backing table is owned by
// the SDK queue package, so it is provisioned here at startup via EnsureSchema
// (advisory-lock guarded, replica-safe) rather than by a hand-written migration.
var initQueue = func(c *Deps) (dim.CleanupFunc, error) {
	c.Queue = dim.OnceWithName("Queue", func(ctx context.Context) (*queue.PG, error) {
		q := queue.NewPG(c.Logger, c.DB(ctx), queue.Options{})
		if err := q.EnsureSchema(ctx); err != nil {
			return nil, fmt.Errorf("ensure queue schema: %w", err)
		}
		return q, nil
	})
	return nil, nil
}
