// Package server assembles the application the lavka-promoaction way: it builds
// the Deps container, runs deps.InitDeps (which registers resource cleanups with
// the global closer), then supervises the enabled transports (REST, gRPC) via a
// worker.Group. Resource shutdown is owned by the global closer; the caller runs
// closer.CloseSync after Run returns.
package server

import (
	"context"
	"net/http"
	"time"

	"github.com/assanoff/servicekit/broker/rabbitmq"
	"github.com/assanoff/servicekit/debugsrv"
	"github.com/assanoff/servicekit/grpcserver"
	"github.com/assanoff/servicekit/health"
	"github.com/assanoff/servicekit/logger"
	"github.com/assanoff/servicekit/metrics"
	"github.com/assanoff/servicekit/outbox"
	"github.com/assanoff/servicekit/worker"

	"github.com/assanoff/service-kit-x/core/widgetaudit"
	"github.com/assanoff/service-kit-x/core/widgetimport"
	widgetv1 "github.com/assanoff/service-kit-x/gen/widget/v1"
	"github.com/assanoff/service-kit-x/internal/app/config"
	"github.com/assanoff/service-kit-x/internal/app/deps"
)

// App is the assembled, runnable application.
type App struct {
	log   *logger.Logger
	group *worker.Group
}

// New builds the application: initializes dependencies (cleanups go to the
// global closer) and registers the enabled transports as supervised runnables.
func New(ctx context.Context, opts config.ServerOpts, log *logger.Logger) (*App, error) {
	d, err := initDeps(opts, log)
	if err != nil {
		return nil, err
	}
	m := metrics.New("servicekit")

	// Debug endpoints (pprof + metrics + health). Here they are attached to the
	// application router (buildRouter mounts debugsrv.Handler at debugsrv.Paths).
	// To instead serve them on a separate internal port, leave embedDebug nil and
	// add a standalone server to the group:
	//
	//	group.Add(debugsrv.New(debugsrv.Config{
	//		Addr: opts.Debug.Addr, Logger: log.Slog(),
	//		MetricsHandler: m.Handler(), Liveness: health.Liveness(), Readiness: readiness(d),
	//	}))
	var embedDebug *debugsrv.Config
	if !opts.Debug.Disabled {
		embedDebug = &debugsrv.Config{
			MetricsHandler: m.Handler(),
			Liveness:       health.Liveness(),
			Readiness:      readiness(d),
		}
	}

	group := worker.NewGroup(log.Slog(), opts.HTTP.ShutdownTimeout)
	if !opts.HTTP.Disabled {
		group.Add(newRestServer(opts.HTTP, log, buildRouter(ctx, d, m, embedDebug)))
	}
	if !opts.GRPC.Disabled {
		group.Add(buildGRPCServer(ctx, opts.GRPC, d, m))
	}
	if !opts.Worker.Disabled {
		group.Add(d.WidgetImport(ctx).NewLoop(widgetimport.Config{
			Interval:  opts.Worker.Interval,
			BatchSize: opts.Worker.BatchSize,
		}))
	}
	if opts.Broker.Enabled {
		if err := addBrokerWorkers(ctx, opts, d, group, m); err != nil {
			return nil, err
		}
	}

	return &App{log: log, group: group}, nil
}

// readiness builds the readiness handler (DB ping) shared by the embedded and
// standalone debug paths.
func readiness(d *deps.Deps) http.Handler {
	return health.Readiness(2*time.Second, health.NamedChecker{
		Name:  "postgres",
		Check: func(ctx context.Context) error { return d.DB(ctx).PingContext(ctx) },
	})
}

// addBrokerWorkers registers the transactional-outbox workers (relay, sweeper,
// cleaner) and the widget-audit consumer that together form the reliable
// publish->consume pipeline. Outbox metrics are registered on the shared
// registry m, so they sit alongside the HTTP/gRPC and any business metrics on
// the same /metrics endpoint without colliding (distinct servicekit_outbox_*
// names).
func addBrokerWorkers(ctx context.Context, opts config.ServerOpts, d *deps.Deps, group *worker.Group, m *metrics.Metrics) error {
	ob := d.Outbox(ctx)
	om := outbox.NewMetrics(m.Registry)
	group.Add(outbox.NewRelay(d.Logger, ob, d.Publisher(ctx), outbox.RelayConfig{Metrics: om}))
	group.Add(outbox.NewSweeper(d.Logger, ob, outbox.SweeperConfig{Metrics: om}))
	group.Add(outbox.NewCleaner(d.Logger, ob, outbox.CleanerConfig{Metrics: om}))

	// Backlog gauges query the store on each scrape; register the collector when
	// the store supports it (the Postgres store does).
	if sr, ok := ob.(outbox.StatsReader); ok {
		m.Registry.MustRegister(outbox.NewBacklogCollector(sr, d.Logger))
	}

	recorder := widgetaudit.New(d.Logger, d.DB(ctx), d.Tracer(ctx))
	consumer, err := rabbitmq.NewConsumer(d.BrokerConn(ctx), d.Logger, rabbitmq.ConsumerConfig{
		Queue:       opts.Broker.Queue,
		Exchange:    opts.Broker.Exchange,
		RoutingKeys: []string{opts.Broker.RoutingKey},
		Name:        "widget-audit",
	}, recorder.Handle)
	if err != nil {
		return err
	}
	group.Add(consumer)
	return nil
}

// Run starts all runnables and blocks until ctx is canceled or one fails.
func (a *App) Run(ctx context.Context) error {
	return a.group.Run(ctx)
}

// Handler builds the REST handler (middleware + technical and business routes).
// Exported so integration tests can drive the HTTP stack via httptest.
func Handler(ctx context.Context, opts config.ServerOpts, log *logger.Logger) (http.Handler, error) {
	d, err := initDeps(opts, log)
	if err != nil {
		return nil, err
	}
	return buildRouter(ctx, d, metrics.New("servicekit"), nil), nil
}

// GRPCServer builds the gRPC server with the widget service registered.
// Exported so integration tests can serve it on a bufconn or local listener.
func GRPCServer(ctx context.Context, opts config.ServerOpts, log *logger.Logger) (*grpcserver.Server, error) {
	d, err := initDeps(opts, log)
	if err != nil {
		return nil, err
	}
	return buildGRPCServer(ctx, opts.GRPC, d, metrics.New("servicekit")), nil
}

// initDeps builds the Deps container and runs the initializers (registering
// cleanups with the global closer).
func initDeps(opts config.ServerOpts, log *logger.Logger) (*deps.Deps, error) {
	d := &deps.Deps{Opts: opts, Logger: log}
	if err := deps.InitDeps(d, deps.Initializers); err != nil {
		return nil, err
	}
	return d, nil
}

func buildGRPCServer(ctx context.Context, cfg config.GRPC, d *deps.Deps, m *metrics.Metrics) *grpcserver.Server {
	const miB = 1 << 20
	gs := grpcserver.New(
		d.Logger, grpcserver.Config{
			Addr:             cfg.Addr,
			ShutdownTimeout:  cfg.ShutdownTimeout,
			EnableReflection: cfg.Reflection,
			MetricsNamespace: "servicekit",
			// Performance: messages use the protobuf Opaque API (edition 2023);
			// tune transport here.
			MaxRecvMsgSize:    cfg.MaxRecvMiB * miB,
			MaxSendMsgSize:    cfg.MaxSendMiB * miB,
			SharedWriteBuffer: true,
			Keepalive: grpcserver.KeepaliveConfig{
				MaxConnectionIdle:   15 * time.Minute,
				Time:                2 * time.Minute,
				Timeout:             20 * time.Second,
				MinTime:             10 * time.Second,
				PermitWithoutStream: true,
			},
		},
		grpcserver.WithTracer(d.Tracer(ctx)),
		grpcserver.WithMetrics(m.Registry),
	)
	widgetv1.RegisterWidgetServiceServer(gs.ServiceRegistrar(), d.WidgetGRPC(ctx))
	return gs
}
