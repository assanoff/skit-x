// Package server assembles the application: it builds
// the Deps container, runs app.InitDeps (which registers resource cleanups with
// the global closer), then composes the enabled transports (REST, gRPC,
// grpc-gateway, status) plus background workers and consumers into one flat
// []worker.Runnable and supervises them via a worker.Group (through app.Run).
// Each brick starts iff its Addr is set, so they run independently.
// Resource shutdown is owned by the global closer; the caller runs
// closer.CloseSync after Run returns.
package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/assanoff/skit/app"
	"github.com/assanoff/skit/broker/rabbitmq"
	"github.com/assanoff/skit/closer"
	"github.com/assanoff/skit/debugsrv"
	"github.com/assanoff/skit/grpcgateway"
	"github.com/assanoff/skit/grpcserver"
	"github.com/assanoff/skit/health"
	"github.com/assanoff/skit/httpserver"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/metrics"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/worker"

	"github.com/assanoff/skit-x/core/widgetaudit"
	"github.com/assanoff/skit-x/core/widgetimport"
	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/deps"
)

// App is the assembled application: the flat set of supervised runnables —
// servers, background workers and queue consumers, all worker.Runnables.
type App struct {
	runnables []worker.Runnable
}

// New builds the application: initializes dependencies (cleanups go to the
// global closer) and registers the enabled transports as supervised runnables.
func New(ctx context.Context, opts config.ServerOpts, log *logger.Logger) (*App, error) {
	d, err := initDeps(opts, log)
	if err != nil {
		return nil, err
	}
	m := metrics.New("skit")

	// Extra runnables: background workers + (optionally) the broker pipeline.
	// They are supervised alongside the servers, appended after the transports.
	var extra []worker.Runnable
	if !opts.Worker.Disabled {
		extra = append(
			extra,
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
		var bw []worker.Runnable
		bw, err = brokerWorkers(ctx, opts, d, m)
		if err != nil {
			return nil, err
		}
		extra = append(extra, bw...)
	}

	gateway, err := buildGateway(ctx, opts, d, log)
	if err != nil {
		return nil, err
	}

	// Compose the supervised runnables as one flat slice: the REST API, gRPC,
	// grpc-gateway and the status server each run as an independent worker.Runnable
	// on its own listener, followed by the background workers and consumers. Each
	// transport is appended only when its Addr is set — clearing an *_ADDR turns it
	// off without a separate kill-switch flag (the addr-gating convention). The
	// status server lives on its own internal port so metrics and probes survive
	// independently of REST (the REST router still exposes /healthz and /readyz for
	// convenience — buildRouter with a nil debug config mounts the probes but not
	// pprof/metrics).
	var runnables []worker.Runnable
	if opts.HTTP.Addr != "" {
		// The REST API is just an http.Handler supervised by the generic httpserver
		// brick (Name "rest-server"); there is no dedicated REST server type.
		runnables = append(runnables, httpserver.New(httpserver.Config{
			Name:              "rest-server",
			Addr:              opts.HTTP.Addr,
			ReadHeaderTimeout: opts.HTTP.ReadHeaderTimeout,
			ShutdownTimeout:   opts.HTTP.ShutdownTimeout,
			Logger:            log.Slog(),
		}, buildRouter(ctx, d, m, nil)))
	}
	if opts.GRPC.Addr != "" {
		runnables = append(runnables, buildGRPCServer(ctx, opts.GRPC, d, m))
	}
	if gateway != nil { // nil when the gateway or the gRPC server it proxies is off
		runnables = append(runnables, gateway)
	}
	if opts.Debug.Addr != "" {
		runnables = append(runnables, debugsrv.New(debugsrv.Config{
			Addr:           opts.Debug.Addr,
			Logger:         log.Slog(),
			MetricsHandler: m.Handler(),
			Liveness:       health.Liveness(),
			Readiness:      readiness(d),
		}))
	}
	runnables = append(runnables, extra...)

	return &App{runnables: runnables}, nil
}

// buildGateway builds the grpc-gateway brick — a JSON/HTTP proxy in front of the
// gRPC server, on its own port — or returns nil when the gateway or the gRPC
// server it proxies to is disabled. The dialed gRPC connection is released via
// the global closer on shutdown.
func buildGateway(ctx context.Context, opts config.ServerOpts, d *deps.Deps, log *logger.Logger) (worker.Runnable, error) {
	if opts.Gateway.Addr == "" || opts.GRPC.Addr == "" {
		return nil, nil
	}
	// The feature owns its generated gateway registrar behind the
	// grpcgateway.HandlerRegistrar seam (widgetgrpc.Handler.RegisterGateway), so
	// this orchestrator does not import the generated widgetv1 package.
	gw, err := grpcgateway.New(ctx, grpcgateway.Config{
		Endpoint: dialTarget(opts.GRPC.Addr),
	}, d.WidgetGRPC(ctx).RegisterGateway)
	if err != nil {
		return nil, err
	}
	closer.Add(gw.Close)
	// Addr is non-empty here (guarded above), so httpserver.New returns a live brick.
	return httpserver.New(httpserver.Config{
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
// publish->consume pipeline, returning them as runnables for the supervised set.
// Outbox metrics are registered on the shared registry m, so they sit alongside
// the HTTP/gRPC and any business metrics on the same /metrics endpoint without
// colliding (distinct skit_outbox_* names).
func brokerWorkers(ctx context.Context, opts config.ServerOpts, d *deps.Deps, m *metrics.Metrics) ([]worker.Runnable, error) {
	ob := d.Outbox(ctx)
	om := outbox.NewMetrics(m.Registry)
	runnables := make([]worker.Runnable, 0, 4)
	runnables = append(runnables,
		outbox.NewRelay(d.Logger, ob, d.Publisher(ctx), outbox.RelayConfig{Metrics: om}),
		outbox.NewSweeper(d.Logger, ob, outbox.SweeperConfig{Metrics: om}),
		outbox.NewCleaner(d.Logger, ob, outbox.CleanerConfig{Metrics: om}),
	)

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

// Runnables returns the supervised transports and workers, ready to hand to
// app.Run (which owns the signal context, worker.Group and closer lifecycle).
// The slice may contain nil entries for disabled (addr-gated) bricks; worker.Group.Add
// skips them.
func (a *App) Runnables() []worker.Runnable { return a.runnables }

// Handler builds the REST handler (middleware + technical and business routes).
// Exported so integration tests can drive the HTTP stack via httptest.
func Handler(ctx context.Context, opts config.ServerOpts, log *logger.Logger) (http.Handler, error) {
	d, err := initDeps(opts, log)
	if err != nil {
		return nil, err
	}
	return buildRouter(ctx, d, metrics.New("skit"), nil), nil
}

// GRPCServer builds the gRPC server with the widget service registered.
// Exported so integration tests can serve it on a bufconn or local listener.
func GRPCServer(ctx context.Context, opts config.ServerOpts, log *logger.Logger) (*grpcserver.Server, error) {
	d, err := initDeps(opts, log)
	if err != nil {
		return nil, err
	}
	return buildGRPCServer(ctx, opts.GRPC, d, metrics.New("skit")), nil
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
			MetricsNamespace: "skit",
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
	// Each feature owns its generated RegisterXxxServer call behind the
	// grpcserver.Service seam, so this orchestrator does not register services
	// by hand — it just hands them the registrar (mirrors the REST Install).
	gs.Install(d.WidgetGRPC(ctx))
	return gs
}
