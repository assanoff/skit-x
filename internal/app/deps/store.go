package deps

import (
	"context"

	"github.com/assanoff/servicekit/dim"
	"github.com/assanoff/servicekit/provider"
	"github.com/assanoff/servicekit/queue"
	"github.com/assanoff/servicekit/sqldb"
)

// initStore initializes the Postgres connection pool and assigns it to the
// container, using the SDK's ready-made Postgres provider.
var initStore = func(c *Deps) (cleanup dim.CleanupFunc, err error) {
	c.DB, cleanup = dim.NewResource("Store", provider.Postgres(sqldb.Config{
		User:         c.Opts.DB.User,
		Password:     c.Opts.DB.Password,
		Host:         c.Opts.DB.Host,
		Name:         c.Opts.DB.Name,
		Schema:       c.Opts.DB.Schema,
		MaxIdleConns: c.Opts.DB.MaxIdleConns,
		MaxOpenConns: c.Opts.DB.MaxOpenConns,
		DisableTLS:   c.Opts.DB.DisableTLS,
	}))
	return cleanup, nil
}

// initQueue builds the Postgres-backed work queue. The backing table is created
// by migrations (0002_queue.sql); the queue itself holds no resources of its own.
var initQueue = func(c *Deps) (dim.CleanupFunc, error) {
	c.Queue = dim.OnceWithName("Queue", func(ctx context.Context) (*queue.PG, error) {
		return queue.NewPG(c.Logger, c.DB(ctx), queue.Options{}), nil
	})
	return nil, nil
}
