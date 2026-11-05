package deps

import (
	"context"
	"time"

	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/poller"
	"github.com/assanoff/skit/settings"

	widgetapi "github.com/assanoff/skit-x/api/widget"
	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widget/widgetdb"
	"github.com/assanoff/skit-x/core/widgetimport"
	"github.com/assanoff/skit-x/internal/app/handlers/widgetgrpc"
)

// initWidgetSettings reads the widget settings, the document "widgets" of the
// table settings, with widget.DefaultSettings for what is not stored. The
// table is owned by the SDK settings package, so it is provisioned here at
// startup via EnsureSchema. A failed read fails the start: the service does
// not run on the defaults in place of the settings an admin saved.
var initWidgetSettings = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetSettings = dim.OnceWithName("WidgetSettings", func(ctx context.Context) (*settings.Settings[widget.Settings], error) {
		db := c.DB(ctx)
		if err := settings.EnsureSchema(ctx, c.Logger, db); err != nil {
			return nil, err
		}
		return settings.New(ctx, c.Logger, db, "widgets", widget.DefaultSettings())
	})
	return nil, nil
}

// initWidgetCore builds the widget business core over the Postgres store. Its
// widget.created event goes into the queue of events (atomic with the widget
// insert) when the broker is enabled, and is dropped when it is not; the route
// is in the queue Registry (c.Tasks).
var initWidgetCore = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetCore = dim.OnceWithName("WidgetCore", func(ctx context.Context) (*widget.Core, error) {
		store := widgetdb.NewStore(c.Logger, c.DB(ctx), widgetdb.WithTasks(c.Tasks(ctx)))

		// In-process dispatch is always on (a bus with no consumers is a no-op).
		// The writes check the settings an admin keeps.
		opts := []widget.Option{
			widget.WithEventBus(c.Bus(ctx)),
			widget.WithMetrics(c.Metrics),
			widget.WithSettings(c.WidgetSettings(ctx).Current),
		}
		return widget.NewCore(c.Logger, store, opts...), nil
	})
	return nil, nil
}

// initWidgetImport builds the widget bulk-import service over the durable queue
// and the widget store (which provides the idempotent BulkInsert); an import
// checks the settings an admin keeps.
var initWidgetImport = func(c *Deps) (dim.CleanupFunc, error) {
	c.WidgetImport = dim.OnceWithName("WidgetImport", func(ctx context.Context) (*widgetimport.Importer, error) {
		store := widgetdb.NewStore(c.Logger, c.DB(ctx))
		current := widgetimport.WithSettings(c.WidgetSettings(ctx).Current)
		return widgetimport.New(c.Logger, c.Queue(ctx), store, current), nil
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
		return poller.New(c.Logger, 0, countAll, poller.Config{
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
		return widgetapi.New(c.WidgetCore(ctx), c.WidgetImport(ctx), c.WidgetCount(ctx), c.WidgetSettings(ctx), c.Translation(ctx), c.AuthVerifier(ctx), c.Opts.Auth.RequiredRole), nil
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
