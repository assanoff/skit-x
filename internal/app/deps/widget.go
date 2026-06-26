package deps

import (
	"context"
	"time"

	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/poller"

	widgetapi "github.com/assanoff/skit-x/api/widget"
	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widget/widgetdb"
	"github.com/assanoff/skit-x/core/widgetimport"
	"github.com/assanoff/skit-x/internal/app/handlers/widgetgrpc"
)

// initWidgetCore builds the widget business core over the Postgres store. When
// the broker is enabled, Create also emits a widget.created event through the
// transactional outbox (atomic with the widget insert). The event's transport
// route is registered here at startup — the domain only emits a typed value.
var initWidgetCore = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetCore = dim.OnceWithName("WidgetCore", func(ctx context.Context) (*widget.Core, error) {
		store := widgetdb.NewStore(c.Logger, c.DB(ctx))

		// In-process dispatch is always on (a bus with no consumers is a no-op);
		// the durable outbox is added only when the broker is enabled.
		opts := []widget.Option{widget.WithEventBus(c.Bus(ctx))}
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

// initWidgetCount builds a poller that caches the total widget count, refreshed
// on an interval, so the GET /widgets/count endpoint serves it cheaply without
// querying Postgres on every request. The poller is a worker.Runnable, supervised
// in the server's worker.Group; Current() reads the cached value lock-free-ish.
var initWidgetCount = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetCount = dim.OnceWithName("WidgetCount", func(ctx context.Context) (*poller.Poller[int], error) {
		core := c.WidgetCore(ctx)
		// Count now takes a filter; the poller caches the unfiltered total, so it
		// passes the zero QueryFilter (matches every widget).
		countAll := func(ctx context.Context) (int, error) {
			return core.Count(ctx, widget.QueryFilter{})
		}
		return poller.New(c.Logger.Slog(), 0, countAll, poller.Config{
			Name:        "widget-count",
			Interval:    c.Opts.Worker.CountInterval,
			PollTimeout: 5 * time.Second,
		}), nil
	})
	return nil, nil
}

// initWidgetHandler builds the REST handler for widgets, including the
// background-import endpoint and the cached-count endpoint.
var initWidgetHandler = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetHandler = dim.OnceWithName("WidgetHandler", func(ctx context.Context) (*widgetapi.Handler, error) {
		return widgetapi.New(c.WidgetCore(ctx), c.WidgetImport(ctx), c.WidgetCount(ctx), c.Translation(ctx), c.AuthVerifier(ctx), c.Opts.Auth.RequiredRole), nil
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
