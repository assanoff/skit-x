package deps

import (
	"context"
	"fmt"

	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/dbx/pg"
	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/provider/postgres"
	"github.com/assanoff/skit/queue"

	"github.com/assanoff/skit-x/core/widget"
)

// EventsQueue is the queue the domain events go through to the broker.
const EventsQueue = "events"

// initStore initializes the Postgres connection pool and assigns it to the
// container through the ready Postgres factory of provider/postgres.
var initStore = func(c *Deps) (cleanup dim.CleanupFunc, err error) {
	c.DB, cleanup = dim.NewResource("Store", postgres.Resource(pg.Config{
		User:           c.Opts.DB.User,
		Password:       c.Opts.DB.Password,
		Host:           c.Opts.DB.Host,
		Name:           c.Opts.DB.Name,
		Schema:         c.Opts.DB.Schema,
		MaxIdleConns:   c.Opts.DB.MaxIdleConns,
		MaxOpenConns:   c.Opts.DB.MaxOpenConns,
		DisableTLS:     !c.Opts.DB.TLS,
		Metrics:        c.Metrics,
		TracerProvider: c.TracerProvider,
	}))
	return cleanup, nil
}

// initQueue builds the Postgres-backed queues and the registry of their
// routes. The backing table is owned by the SDK queue package, so it is
// provisioned here at startup via EnsureSchema (advisory-lock guarded,
// replica-safe) rather than by a hand-written migration.
var initQueue = func(c *Deps) (dim.CleanupFunc, error) {
	c.Queue = dim.OnceWithName("Queue", func(ctx context.Context) (*queue.PG, error) {
		q := queue.NewPG(c.Logger, c.DB(ctx))
		if err := q.EnsureSchema(ctx); err != nil {
			return nil, fmt.Errorf("ensure queue schema: %w", err)
		}
		return q, nil
	})
	// The routes are wiring: the domain only schedules a typed value.
	c.Registry = dim.OnceWithName("Registry", func(context.Context) (*queue.Registry, error) {
		reg := queue.NewRegistry()
		err := reg.Register[widget.Created](EventsQueue, widget.EventWidgetCreated, c.EventRoute())
		if err != nil {
			return nil, fmt.Errorf("route widget.created: %w", err)
		}
		return reg, nil
	})
	return nil, nil
}

// EventRoute is the route option of the events: their outbox envelope while
// the broker is on, Drop while it is off — the domain write goes through, the
// event goes nowhere, and the domain does not branch.
func (c *Deps) EventRoute() queue.RouteOption {
	if !c.Opts.Broker.Enabled() {
		return queue.Drop()
	}
	return queue.WithEncoder(outbox.Envelope())
}

// Tasks is what a store binds its tasks with (WithTasks): a Scheduler on the
// transaction, which routes each value by the Registry.
func (c *Deps) Tasks(ctx context.Context) func(tx *dbx.Tx) *queue.Scheduler {
	q, reg := c.Queue(ctx), c.Registry(ctx)
	return func(tx *dbx.Tx) *queue.Scheduler {
		return queue.Bind(tx, q, reg)
	}
}
