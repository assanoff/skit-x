package deps

import (
	"context"
	"fmt"

	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/broker/rabbitmq"
	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/outbox"
)

// initBroker dials RabbitMQ and builds the publisher + outbox store when the
// broker is enabled. The connection and publisher are created eagerly (fail
// fast on bad config) and their cleanups registered with the global closer; the
// providers expose them to the server for the relay/consumer wiring. When the
// broker is disabled this is a no-op and the providers stay nil.
var initBroker = func(c *Deps) (dim.CleanupFunc, error) {
	bcfg := c.Opts.Broker
	if !bcfg.Enabled {
		return nil, nil
	}

	conn, err := rabbitmq.Dial(c.Logger, rabbitmq.Config{
		User:     bcfg.User,
		Password: bcfg.Password,
		Host:     bcfg.Host,
		Port:     bcfg.Port,
	})
	if err != nil {
		return nil, err
	}

	pub, err := rabbitmq.NewPublisher(conn, bcfg.Source, c.Logger)
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
	c.Outbox = dim.OnceWithName("Outbox", func(ctx context.Context) (outbox.Store, error) {
		store := outbox.NewPG(c.Logger, c.DB(ctx), outbox.Options{})
		if err := store.EnsureSchema(ctx); err != nil {
			return nil, fmt.Errorf("ensure outbox schema: %w", err)
		}
		return store, nil
	})

	// Closed LIFO: publisher first, then the connection it rode on.
	cleanup := func() error {
		_ = pub.Close()
		return conn.Close()
	}
	return cleanup, nil
}
