// Package server assembles the application from the options a command lists.
// New builds the Deps container — app.InitDeps registers resource cleanups with
// the global closer — and then applies the options (see options.go): HTTP
// servers with their route sets, the gRPC server and its gateway, background
// workers, queue consumers. app.Run then supervises what they added and runs
// closer.CloseSync after it returns.
package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/assanoff/skit/app"
	"github.com/assanoff/skit/broker/rabbitmq"
	"github.com/assanoff/skit/closer"
	"github.com/assanoff/skit/grpcgateway"
	"github.com/assanoff/skit/grpcserver"
	"github.com/assanoff/skit/health"
	"github.com/assanoff/skit/httpserver"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/metrics"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/rest"
	"github.com/assanoff/skit/worker"

	"github.com/assanoff/skit-x/core/widgetaudit"
	"github.com/assanoff/skit-x/core/widgetimport"
	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/deps"
)

// App is the assembled application: what the process runs, by kind. Every part
// is a worker.Runnable; Runnables hands them all to app.Run.
type App struct {
	opts config.ServerOpts
	log  *logger.Logger
	d    *deps.Deps
	m    *metrics.Metrics

	// mids is the application middleware of the business routes, built by
	// the first Install (see appMids).
	mids []rest.MidFunc

	servers   []listener
	workers   []worker.Runnable
	consumers []worker.Runnable
}

// listener is a server the process runs: an httpserver.Server (WithServer,
// WithGateway) or the gRPC server (WithGRPC). Addr is what Components reports.
type listener interface {
	worker.Runnable
	Addr() string
}

// New builds the application a command asked for: the dependency container,
// then the options it lists (see options.go), in that order. Without options
// it builds the container only. Nothing starts here — app.Run does that.
func New(ctx context.Context, opts config.ServerOpts, log *logger.Logger, options ...Option) (*App, error) {
	d, err := initDeps(opts, log)
	if err != nil {
		return nil, err
	}
	a := newApp(opts, log, d)

	for _, add := range options {
		if err := add(ctx, a); err != nil {
			// app.Run never runs, so release what the options already built.
			return nil, errors.Join(err, closer.CloseSync())
		}
	}
	return a, nil
}

func newApp(opts config.ServerOpts, log *logger.Logger, d *deps.Deps) *App {
	return &App{opts: opts, log: log, d: d, m: metrics.New("skit")}
}

// Runnables returns the servers, workers and consumers, ready to hand to
// app.Run (which owns the signal context, worker.Group and closer lifecycle).
func (a *App) Runnables() []worker.Runnable {
	rs := make([]worker.Runnable, 0, len(a.servers)+len(a.workers)+len(a.consumers))
	for _, s := range a.servers {
		rs = append(rs, s)
	}
	rs = append(rs, a.workers...)
	return append(rs, a.consumers...)
}

// Components is what the process runs, as its command composed it: where it
// listens and how many workers and consumers it has. The command logs it at
// startup and GET /debug/components serves it, so a pod tells what it does.
type Components struct {
	Servers   []string `json:"servers"`
	Workers   int      `json:"workers"`
	Consumers int      `json:"consumers"`
}

// Components reports what Runnables returns — kill switches included.
func (a *App) Components() Components {
	addrs := make([]string, 0, len(a.servers))
	for _, s := range a.servers {
		addrs = append(addrs, s.Addr())
	}
	return Components{Servers: addrs, Workers: len(a.workers), Consumers: len(a.consumers)}
}

// initWorkers registers the background work: the domain loops and — when the
// broker is enabled — the transactional-outbox workers (relay, sweeper, cleaner)
// that poll the database.
//
// Run it as a single replica: these loops poll the same rows, so extra replicas
// only add database load (and any wall-clock job would fire twice).
//
// Worker.Disabled and Broker.Enabled are configuration, not composition: the
// command decides whether WithWorkers is in its list at all, the switches decide
// which of its workers start.
func (a *App) initWorkers(ctx context.Context) error {
	if !a.opts.Worker.Disabled {
		a.workers = append(a.workers,
			a.d.WidgetImport(ctx).NewLoop(widgetimport.Config{
				Interval:  a.opts.Worker.Interval,
				BatchSize: a.opts.Worker.BatchSize,
			}),
			// The widget-count poller refreshes the cached count served by
			// GET /widgets/count; it is a worker.Runnable.
			a.d.WidgetCount(ctx),
		)
	}
	if !a.opts.Broker.Enabled {
		return nil
	}

	ob := a.d.Outbox(ctx)
	// Outbox metrics are registered on the shared registry, so they sit alongside
	// the HTTP/gRPC and business metrics on the same /metrics endpoint without
	// colliding (distinct skit_outbox_* names).
	om := outbox.NewMetrics(a.m.Registry)
	a.workers = append(a.workers,
		outbox.NewRelay(a.d.Logger, ob, a.d.Publisher(ctx), outbox.RelayConfig{Metrics: om}),
		outbox.NewSweeper(a.d.Logger, ob, outbox.SweeperConfig{Metrics: om}),
		outbox.NewCleaner(a.d.Logger, ob, outbox.CleanerConfig{Metrics: om}),
	)

	// Backlog gauges query the store on each scrape; register the collector when
	// the store supports it (the Postgres store does).
	if sr, ok := ob.(outbox.StatsReader); ok {
		a.m.Registry.MustRegister(outbox.NewBacklogCollector(sr, a.d.Logger))
	}
	return nil
}

// initConsumers registers the queue consumers — the receiving half of the
// reliable publish->consume pipeline whose sending half (the outbox workers)
// belongs to initWorkers. Competing consumers, so more than one replica is safe.
func (a *App) initConsumers(ctx context.Context) error {
	if !a.opts.Broker.Enabled {
		return nil
	}

	recorder := widgetaudit.New(a.d.Logger, a.d.DB(ctx), a.d.Tracer(ctx))
	consumer, err := rabbitmq.NewConsumer(a.d.BrokerConn(ctx), a.d.Logger, rabbitmq.ConsumerConfig{
		Queue:       a.opts.Broker.Queue,
		Exchange:    a.opts.Broker.Exchange,
		RoutingKeys: []string{a.opts.Broker.RoutingKey},
		Name:        "widget-audit",
	}, recorder.Handle)
	if err != nil {
		return err
	}
	a.consumers = append(a.consumers, consumer)
	return nil
}

// buildGateway builds the grpc-gateway brick on addr — a JSON/HTTP proxy in
// front of the gRPC server listening on grpcAddr. The dialed gRPC connection is
// released via the global closer on shutdown.
func buildGateway(ctx context.Context, a *App, addr, grpcAddr string) (*httpserver.Server, error) {
	// The feature owns its generated gateway registrar behind the
	// grpcgateway.HandlerRegistrar seam (widgetgrpc.Handler.RegisterGateway), so
	// this orchestrator does not import the generated widgetv1 package.
	gw, err := grpcgateway.New(ctx, grpcgateway.Config{
		Endpoint: dialTarget(grpcAddr),
	}, a.d.WidgetGRPC(ctx).RegisterGateway)
	if err != nil {
		return nil, err
	}
	closer.Add(gw.Close)
	return httpserver.New(httpserver.Config{
		Name:            "gateway-server",
		Addr:            addr,
		ShutdownTimeout: a.opts.GRPC.ShutdownTimeout,
		Logger:          a.log.Slog(),
	}, gw), nil
}

// dialTarget makes a listen address dialable: ":9090" -> "localhost:9090".
func dialTarget(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}

// readiness builds the readiness handler (DB ping).
func readiness(d *deps.Deps) http.Handler {
	return health.Readiness(2*time.Second, health.NamedChecker{
		Name: "postgres",
		Check: func(ctx context.Context) error {
			return d.DB(ctx).PingContext(ctx)
		},
	})
}

// Handler builds the technical and the business routes on one router — the
// Debug and Install route sets, assembled by the same code as WithServer.
// Exported so integration tests can drive the HTTP stack via httptest.
func Handler(ctx context.Context, opts config.ServerOpts, log *logger.Logger) (http.Handler, error) {
	d, err := initDeps(opts, log)
	if err != nil {
		return nil, err
	}
	return newApp(opts, log, d).handler(ctx, Debug, Install), nil
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
