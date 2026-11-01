package deps

import (
	"context"
	"fmt"

	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/broker/rabbitmq"
	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/queue"

	"github.com/assanoff/skit-x/core/widget"
)

// initBroker dials RabbitMQ and builds the publisher and the Mux of the queue
// of events when the broker is enabled. The connection and publisher are
// created eagerly (fail fast on bad config) and their cleanups registered with
// the global closer; the providers expose them to the server for the event
// workers and the consumers. When the broker is disabled this is a no-op and
// the providers stay nil.
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

	// The publisher records a send span per message; the events go out under
	// the trace of the request that created them.
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
	// Each event of the queue of events is published to its topic, the
	// exchange of the service.
	c.Events = dim.OnceWithName("Events", func(context.Context) (*queue.Mux, error) {
		mux := queue.NewMux()
		err := mux.Register(widget.EventWidgetCreated, outbox.Handler(pub, bcfg.Exchange))
		if err != nil {
			return nil, fmt.Errorf("publish widget.created: %w", err)
		}
		return mux, nil
	})

	// Closed LIFO: publisher first, then the connection it rode on.
	cleanup := func() error {
		_ = pub.Close()
		return conn.Close()
	}
	return cleanup, nil
}
