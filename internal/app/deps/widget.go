package deps

import (
	"context"

	"github.com/assanoff/servicekit/dim"
	"github.com/assanoff/servicekit/outbox"

	widgetapi "github.com/assanoff/service-kit-x/api/widget"
	"github.com/assanoff/service-kit-x/core/widget"
	"github.com/assanoff/service-kit-x/core/widget/widgetdb"
	"github.com/assanoff/service-kit-x/core/widgetimport"
	"github.com/assanoff/service-kit-x/internal/app/handlers/widgetgrpc"
)

// initWidgetCore builds the widget business core over the Postgres store. When
// the broker is enabled, Create also emits a widget.created event through the
// transactional outbox (atomic with the widget insert). The event's transport
// route is registered here at startup — the domain only emits a typed value.
var initWidgetCore = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetCore = dim.OnceWithName("WidgetCore", func(ctx context.Context) (*widget.Core, error) {
		store := widgetdb.NewStore(c.Logger, c.DB(ctx))
		var opts []widget.Option
		if c.Opts.Broker.Enabled {
			reg := outbox.NewRegistry()
			outbox.Register[widget.Created](reg,
				widget.EventWidgetCreated, c.Opts.Broker.Exchange,
				outbox.WithKey(c.Opts.Broker.RoutingKey))
			opts = append(opts, widget.WithOutbox(c.DB(ctx), c.Outbox(ctx), reg))
		}
		return widget.NewCore(c.Logger, store, opts...), nil
	})
	return nil, nil
}

// initWidgetImport builds the widget bulk-import service over the durable queue
// and the widget store (which provides the idempotent BulkInsert).
var initWidgetImport = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetImport = dim.OnceWithName("WidgetImport", func(ctx context.Context) (*widgetimport.Importer, error) {
		store := widgetdb.NewStore(c.Logger, c.DB(ctx))
		return widgetimport.New(c.Logger, c.Queue(ctx), store), nil
	})
	return nil, nil
}

// initWidgetHandler builds the REST handler for widgets, including the
// background-import endpoint.
var initWidgetHandler = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetHandler = dim.OnceWithName("WidgetHandler", func(ctx context.Context) (*widgetapi.Handler, error) {
		return widgetapi.New(c.WidgetCore(ctx), c.WidgetImport(ctx)), nil
	})
	return nil, nil
}

// initWidgetGRPC builds the gRPC handler for widgets.
var initWidgetGRPC = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetGRPC = dim.OnceWithName("WidgetGRPC", func(ctx context.Context) (*widgetgrpc.Handler, error) {
		return widgetgrpc.New(c.WidgetCore(ctx)), nil
	})
	return nil, nil
}
