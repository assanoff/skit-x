// Package server assembles the application the lavka-promoaction way: it builds
// the Deps container, runs app.InitDeps (which registers resource cleanups with
// the global closer), then composes the enabled transports (REST, gRPC,
// grpc-gateway, status) into a servicekit server.Set and supervises them via a
// worker.Group. Each brick starts iff its Addr is set, so they run independently.
// Resource shutdown is owned by the global closer; the caller runs
// closer.CloseSync after Run returns.
package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/assanoff/servicekit/app"
	"github.com/assanoff/servicekit/broker/rabbitmq"
	"github.com/assanoff/servicekit/closer"
	"github.com/assanoff/servicekit/debugsrv"
	"github.com/assanoff/servicekit/grpcgateway"
	"github.com/assanoff/servicekit/grpcserver"
	"github.com/assanoff/servicekit/health"
	"github.com/assanoff/servicekit/logger"
	"github.com/assanoff/servicekit/metrics"
	"github.com/assanoff/servicekit/outbox"
	"github.com/assanoff/servicekit/server"
	"github.com/assanoff/servicekit/web/httpserver"
	"github.com/assanoff/servicekit/web/rest"
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

	// Extra runnables: background workers + (optionally) the broker pipeline.
	// They are supervised alongside the servers, after the four named bricks.
	var extra []worker.Runnable
	if !opts.Worker.Disabled {
		extra = append(extra,
			d.WidgetImport(ctx).NewLoop(widgetimport.Config{
				Interval:  opts.Worker.Interval,
				BatchSize: opts.Worker.BatchSize,
			}),
			// The widget-count poller refreshes the cached count served by
			// GET /widgets/count; it is a worker.Runnable.
			d.WidgetCount(ctx),
		)
	}
	if opts.Broker.Enabled {
		bw, err := brokerWorkers(ctx, opts, d, m)
		if err != nil {
			return nil, err
		}
		extra = append(extra, bw...)
	}

	gateway, err := buildGateway(ctx, opts, log)
	if err != nil {
		return nil, err
	}

	// Compose the server set: REST, gRPC, grpc-gateway and the status server each
	// run as independent worker.Runnables, each on its own listener. A brick is
	// included only when its Addr is set (server.REST/Enabled/HTTP/Status return
	// nil for an empty Addr), so an empty *_ADDR turns that transport off without
	// a kill-switch flag. The status server lives on its own internal port so
	// metrics and probes survive independently of REST (the REST router still
	// exposes /healthz and /readyz for convenience — buildRouter with a nil debug
	// config mounts the probes but not pprof/metrics).
	set := server.Set{
		REST: server.REST(rest.ServerConfig{
			Addr:              opts.HTTP.Addr,
			ReadHeaderTimeout: opts.HTTP.ReadHeaderTimeout,
			ShutdownTimeout:   opts.HTTP.ShutdownTimeout,
			Logger:            log.Slog(),
		}, buildRouter(ctx, d, m, nil)),
		GRPC:    server.Enabled(opts.GRPC.Addr, buildGRPCServer(ctx, opts.GRPC, d, m)),
		Gateway: gateway,
		Status: server.Status(debugsrv.Config{
			Addr:           opts.Debug.Addr,
			Logger:         log.Slog(),
			MetricsHandler: m.Handler(),
			Liveness:       health.Liveness(),
			Readiness:      readiness(d),
		}),
		Extra: extra,
	}

	return &App{log: log, group: set.Group(log.Slog(), opts.HTTP.ShutdownTimeout)}, nil
}

// buildGateway builds the grpc-gateway brick — a JSON/HTTP proxy in front of the
// gRPC server, on its own port — or returns nil when the gateway or the gRPC
// server it proxies to is disabled. The dialed gRPC connection is released via
// the global closer on shutdown.
func buildGateway(ctx context.Context, opts config.ServerOpts, log *logger.Logger) (worker.Runnable, error) {
	if opts.Gateway.Addr == "" || opts.GRPC.Addr == "" {
		return nil, nil
	}
	gw, err := grpcgateway.New(ctx, grpcgateway.Config{
		Endpoint: dialTarget(opts.GRPC.Addr),
	}, widgetv1.RegisterWidgetServiceHandler)
	if err != nil {
		return nil, err
	}
	closer.Add(gw.Close)
	return server.HTTP(httpserver.Config{
		Name:            "gateway-server",
		Addr:            opts.Gateway.Addr,
		ShutdownTimeout: opts.GRPC.ShutdownTimeout,
		Logger:          log.Slog(),
	}, gw), nil
}

// dialTarget makes a listen address dialable: ":9090" -> "localhost:9090".
func dialTarget(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}

// readiness builds the readiness handler (DB ping) shared by the embedded and
// standalone debug paths.
func readiness(d *deps.Deps) http.Handler {
	return health.Readiness(2*time.Second, health.NamedChecker{
		Name:  "postgres",
		Check: func(ctx context.Context) error { return d.DB(ctx).PingContext(ctx) },
	})
}

// brokerWorkers builds the transactional-outbox workers (relay, sweeper,
// cleaner) and the widget-audit consumer that together form the reliable
// publish->consume pipeline, returning them as runnables for the server set's
// Extra slice. Outbox metrics are registered on the shared registry m, so they
// sit alongside the HTTP/gRPC and any business metrics on the same /metrics
// endpoint without colliding (distinct servicekit_outbox_* names).
func brokerWorkers(ctx context.Context, opts config.ServerOpts, d *deps.Deps, m *metrics.Metrics) ([]worker.Runnable, error) {
	ob := d.Outbox(ctx)
	om := outbox.NewMetrics(m.Registry)
	runnables := []worker.Runnable{
		outbox.NewRelay(d.Logger, ob, d.Publisher(ctx), outbox.RelayConfig{Metrics: om}),
		outbox.NewSweeper(d.Logger, ob, outbox.SweeperConfig{Metrics: om}),
		outbox.NewCleaner(d.Logger, ob, outbox.CleanerConfig{Metrics: om}),
	}

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
		return nil, err
	}
	return append(runnables, consumer), nil
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
	if err := app.InitDeps(d, deps.Initializers); err != nil {
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
