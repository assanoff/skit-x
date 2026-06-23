package deps

import (
	"context"

	"github.com/assanoff/servicekit/dim"
	"github.com/assanoff/servicekit/queue"

	"github.com/assanoff/service-kit-x/internal/app/provider"
)

// initStore initializes the Postgres connection pool and assigns it to the container.
var initStore = func(c *Deps) (cleanup dim.CleanupFunc, err error) {
	c.DB, cleanup = dim.NewResource("Store", provider.Postgres(&c.Opts, c.Logger))
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
