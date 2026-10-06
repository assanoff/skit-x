package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/assanoff/skit/debugsrv"
	"github.com/assanoff/skit/health"
	"github.com/assanoff/skit/httpserver"
	"github.com/assanoff/skit/middleware"
	"github.com/assanoff/skit/rest"
	"github.com/assanoff/skit/rest/router"
)

// Option adds one part to the process: a server, the workers, the consumers. A
// long-running command in internal/cmd lists the options it runs — servers with
// their addresses and route sets, workers, consumers:
//
//	run(c.ServerOpts,
//		server.WithServer(c.Debug.Addr, server.Debug),
//		server.WithWorkers(),
//	)
//
// New applies them in that order to the App it has just built, and app.Run
// starts whatever they added. Nothing decides the layout behind the command's
// back: a process listens exactly where its command says.
type Option func(ctx context.Context, a *App) error

// Routes is a route set a server mounts: Install (the business API) or one of
// the technical sets — Health, Metrics, Pprof, Info, or Debug for all of them.
// Any set can go on any server; one set must not be listed twice on one server
// (the mux panics on the duplicate pattern).
type Routes func(ctx context.Context, a *App, r *router.Router)

// WithServer adds an HTTP server on addr serving the given route sets; an empty
// addr adds none, so clearing HTTP_ADDR or DEBUG_ADDR turns that server off.
// The server adds only panic recovery to a request (see handler); whatever
// else a request goes through belongs to its route set — Install brings the
// request middleware chain, the technical sets go without one. So a set
// behaves the same on whichever server it is mounted, and a command lists its
// servers the way it wants them:
//
//	server.WithServer(c.HTTP.Addr, server.Install), // the API on its port,
//	server.WithServer(c.Debug.Addr, server.Debug),  // the technical sets on an internal one
//
//	server.WithServer(c.HTTP.Addr, server.Debug, server.Install) // or one port
//
// A process without the API needs a server with Health: that keeps it
// probeable and alive until SIGTERM.
func WithServer(addr string, routes ...Routes) Option {
	return func(ctx context.Context, a *App) error {
		if addr == "" {
			return nil
		}
		a.servers = append(a.servers, httpserver.New(httpserver.Config{
			Name:              "http-server " + addr,
			Addr:              addr,
			ReadHeaderTimeout: a.opts.HTTP.ReadHeaderTimeout,
			ShutdownTimeout:   a.opts.HTTP.ShutdownTimeout,
			Logger:            a.log.Slog(),
			ErrorLog:          slog.NewLogLogger(a.log.Slog().Handler(), slog.LevelError),
		}, a.handler(ctx, routes...)))
		return nil
	}
}

// WithGRPC adds the gRPC server on addr; an empty addr adds none, so clearing
// GRPC_ADDR turns it off.
func WithGRPC(addr string) Option {
	return func(ctx context.Context, a *App) error {
		if addr == "" {
			return nil
		}
		cfg := a.opts.GRPC
		cfg.Addr = addr
		a.servers = append(a.servers, buildGRPCServer(ctx, cfg, a.d, a.m))
		return nil
	}
}

// WithGateway adds the grpc-gateway on addr — a JSON/HTTP proxy in front of the
// gRPC server on grpcAddr. Either address empty adds none: the gateway is off,
// or there is no gRPC server for it to proxy.
func WithGateway(addr, grpcAddr string) Option {
	return func(ctx context.Context, a *App) error {
		if addr == "" || grpcAddr == "" {
			return nil
		}
		gw, err := buildGateway(ctx, a, addr, grpcAddr)
		if err != nil {
			return err
		}
		a.servers = append(a.servers, gw)
		return nil
	}
}

// WithWorkers adds the background workers (see initWorkers). Run them in a
// single replica.
func WithWorkers() Option {
	return func(ctx context.Context, a *App) error {
		return a.initWorkers(ctx)
	}
}

// WithConsumers adds the queue consumers (see initConsumers). More than one
// replica is safe.
func WithConsumers() Option {
	return func(ctx context.Context, a *App) error {
		return a.initConsumers(ctx)
	}
}

// handler builds what a WithServer server serves: the given route sets on one
// router behind a single panic recoverer — the root of every request, the
// technical sets' and the 404s included. Anything more a request goes through
// — transport middleware, application middleware — is up to its set.
func (a *App) handler(ctx context.Context, routes ...Routes) *router.Router {
	r := router.New()
	r.Use(middleware.Panics(a.log))
	for _, mount := range routes {
		mount(ctx, a, r)
	}
	return r
}

// Debug is every technical route set: Health, Metrics, Pprof and Info.
func Debug(ctx context.Context, a *App, r *router.Router) {
	Health(ctx, a, r)
	Metrics(ctx, a, r)
	Pprof(ctx, a, r)
	Info(ctx, a, r)
}

// Health is the Kubernetes probes: /healthz and /readyz (a DB ping).
func Health(_ context.Context, a *App, r *router.Router) {
	r.Handle("GET /healthz", health.Liveness())
	r.Handle("GET /readyz", readiness(a.d))
}

// Metrics is the Prometheus endpoint /metrics.
func Metrics(_ context.Context, a *App, r *router.Router) {
	r.Handle("GET /metrics", a.m.Handler())
}

// Pprof is net/http/pprof under /debug/pprof/. Keep it off a public port.
func Pprof(_ context.Context, _ *App, r *router.Router) {
	r.Handle("/debug/pprof/", debugsrv.Pprof())
}

// Info is what this process runs: GET /debug/components serves Components —
// the same values the command logs at startup.
func Info(_ context.Context, a *App, r *router.Router) {
	r.HandleApp("GET /debug/components", func(context.Context, *http.Request) rest.ResponseEncoder {
		return rest.JSON(a.Components())
	})
}
