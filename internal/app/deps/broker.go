package deps

import (
	"context"
	"fmt"

	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/broker/rabbitmq"
	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/outbox"

	"github.com/assanoff/skit-x/core/widget"
)

// initBroker dials RabbitMQ and builds the publisher, the outbox store and its
// event registry when the broker is enabled. The connection and publisher are created eagerly (fail
// fast on bad config) and their cleanups registered with the global closer; the
// providers expose them to the server for the relay/consumer wiring. When the
// broker is disabled this is a no-op and the providers stay nil.
var initBroker = func(c *Deps) (dim.CleanupFunc, error) {
	bcfg := c.Opts.Broker
	if !bcfg.Enabled() {
		return nil, nil
	}

	conn, err := rabbitmq.Dial(rabbitmq.Config{
		User:     bcfg.User,
		Password: bcfg.Password,
		Host:     bcfg.Host,
		Port:     bcfg.Port,
	})
	if err != nil {
		return nil, err
	}

	// The publisher records a send span per message; the relay's events go
	// out under the trace of the request that created them.
	pub, err := rabbitmq.NewPublisher(conn, rabbitmq.PublisherConfig{
		Source:         bcfg.Source,
		TracerProvider: c.TracerProvider,
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	c.BrokerConn = dim.OnceWithName("BrokerConn", func(context.Context) (*rabbitmq.Conn, error) {
		return conn, nil
	})
	c.Publisher = dim.OnceWithName("Publisher", func(context.Context) (broker.Publisher, error) {
		return pub, nil
	})
	c.Outbox = dim.OnceWithName("Outbox", func(ctx context.Context) (*outbox.PG, error) {
		store := outbox.NewPG(c.Logger, c.DB(ctx), outbox.Options{})
		if err := store.EnsureSchema(ctx); err != nil {
			return nil, fmt.Errorf("ensure outbox schema: %w", err)
		}
		return store, nil
	})
	// The event routes are wiring: the domain only emits a typed value.
	c.Registry = dim.OnceWithName("Registry", func(context.Context) (*outbox.Registry, error) {
		reg := outbox.NewRegistry()
		if err := reg.Register[widget.Created](widget.EventWidgetCreated, bcfg.Exchange); err != nil {
			return nil, fmt.Errorf("register widget.created route: %w", err)
		}
		return reg, nil
	})

	// Closed LIFO: publisher first, then the connection it rode on.
	cleanup := func() error {
		_ = pub.Close()
		return conn.Close()
	}
	return cleanup, nil
}

// Events is what a store binds its events with (WithEvents): the outbox in the
// transaction when the broker is on, nothing when it is off — the domain does
// not branch.
func (c *Deps) Events(ctx context.Context) func(tx *dbx.Tx) *outbox.Publisher {
	if c.Outbox == nil {
		return func(*dbx.Tx) *outbox.Publisher { return outbox.Discard() }
	}
	ob, reg := c.Outbox(ctx), c.Registry(ctx)
	return func(tx *dbx.Tx) *outbox.Publisher {
		return outbox.Bind(tx, ob, reg)
	}
}
