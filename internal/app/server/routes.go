package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/assanoff/skit/auditlog"
	"github.com/assanoff/skit/auditlog/auditrest"
	"github.com/assanoff/skit/auth"
	"github.com/assanoff/skit/debugsrv"
	"github.com/assanoff/skit/errs"
	"github.com/assanoff/skit/health"
	"github.com/assanoff/skit/httplog"
	"github.com/assanoff/skit/metrics"
	"github.com/assanoff/skit/middleware"
	"github.com/assanoff/skit/rest"
	"github.com/assanoff/skit/rest/mid"
	"github.com/assanoff/skit/rest/router"
	"github.com/assanoff/skit/translation/translationrest"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/deps"
	"github.com/assanoff/skit-x/internal/app/reqctx"
)

// buildRouter constructs the HTTP handler: it builds the router shell (the
// global, cross-cutting middleware) and then hands it to Install, which
// describes every route in one place. Cross-cutting middleware
// (trace/access-log/i18n) is global; the request-scoped middleware (metrics,
// timeout, body limit) is applied only to business routes via a sub-group, so
// it does not wrap the debug routes — a request timeout would otherwise cut off
// long pprof profiles.
//
// debug is non-nil when the debug routes (pprof + metrics + health) are attached
// to this router (the default); when nil they run on a separate debug server and
// only the liveness/readiness probes are exposed here for Kubernetes.
func buildRouter(ctx context.Context, d *deps.Deps, m *metrics.Metrics, debug *debugsrv.Config) http.Handler {
	log := d.Logger
	tracer := d.Tracer(ctx)
	translator := d.Translator(ctx)

	// App middleware applied to every typed handler (outermost first):
	//   - reqctx parses the cross-cutting request headers ONCE (language, tenant,
	//     city) into the context; everything below reads from there instead of
	//     re-parsing headers.
	//   - mid.LocalizeErrors localizes any *errs.Error response into the
	//     request language (resolved by reqctx.Language).
	//   - mid.Chain bundles, in order: Metrics (errs-aware outcome counter) ->
	//     LocalizeErrors -> MaskInternal (hides 5xx detail from clients in
	//     production) -> Errors (records 5xx on the trace span + logs them) ->
	//     Panics (turns a handler panic into a clean Internal error).
	//   - translationrest translates per-record content: when a handler returns a
	//     translation.Translatable (or TranslatableList) it applies the stored
	//     translation in place — so widget read handlers stay translation-agnostic.
	// Audit recording is NOT a transport concern here — the widget domain emits
	// audit events on the eventbus (see core/widget), which covers REST, gRPC and
	// background paths uniformly.
	//
	// The app middleware is assembled as: reqctx (outermost — parses the language
	// before anything reads it) -> mid.Chain -> translationrest (innermost).
	// Chain is the SDK-standard core; reqctx and translation are app-specific and
	// wrap it.
	appMids := make([]rest.MidFunc, 0, 3)
	appMids = append(appMids, reqctx.Middleware())
	appMids = append(appMids, mid.Chain(mid.Config{
		Translator:   translator,
		Lang:         reqctx.Language,
		Logger:       log,
		MaskInternal: d.Opts.Env == "production",
		RecordMetric: errorOutcomeRecorder(m),
	})...)
	appMids = append(appMids, translationrest.MiddlewareWithLang(log, d.Translation(ctx), reqctx.Language))
	r := router.New(appMids...)

	// Global middleware — safe for every route including debug. Access logging
	// via the vendored httplog (tagged logger=access) also recovers panics, so
	// it replaces a separate recovery middleware. TraceRequest runs first so
	// trace_id is present.
	httpLogOpts := &httplog.Options{
		Level:         slog.LevelInfo,
		Schema:        httplog.SchemaOTEL.Concise(true),
		RecoverPanics: true,
	}
	r.Use(
		middleware.TraceRequest(tracer),
		httplog.Middleware(log.Named("access"), httpLogOpts),
	)

	Install(ctx, r, d, m, debug)
	return r
}

// Install describes every HTTP route on r. It is the single place that composes
// the application's transports: it mounts the technical probes and debug routes,
// builds the request-scoped business sub-group, and delegates each feature's
// route registration to its Routes(handle, ...) method via the router's typed
// registration seam (router.HandleApp, a rest.Handle). Feature handlers
// therefore never depend
// on the router type — Install owns route composition (grouping, middleware),
// the feature owns its endpoints.
func Install(ctx context.Context, r *router.Router, d *deps.Deps, m *metrics.Metrics, debug *debugsrv.Config) {
	r.HandleFunc("GET /healthz", health.Liveness())
	r.Handle("GET /readyz", readiness(d))

	// Debug routes (pprof/metrics/health) attach to the app router when embedded;
	// otherwise expose just the probes here. The debug handler is registered on
	// the root group so the request-scoped middleware below does NOT wrap them
	// (a request timeout would cut off long pprof profiles).
	if debug != nil {
		dh := debugsrv.Handler(*debug)
		for _, p := range debugsrv.Paths {
			r.Handle(p, dh)
		}
	}

	// Business endpoints carry the request-scoped middleware (HTTP metrics, per
	// request timeout, body-size limit). handle is the registration seam handed
	// to every feature: it inherits this sub-group's middleware and the router's
	// app middleware.
	api := r.With(
		m.Middleware(),
		middleware.Timeout(d.Opts.HTTP.RequestTimeout),
		middleware.SizeLimit(d.Opts.HTTP.BodySizeLimit),
	)
	handle := api.HandleApp

	// Authorization is a single guard middleware: auth.Guard composes Authenticate
	// + RequireRole (via rest.Chain) into one rest.MidFunc. d.AuthVerifier is nil
	// when auth is disabled and auth.Guard then returns nil — a no-op the Handle
	// seam skips — so no Auth.Enabled check is needed here: a nil guard leaves every
	// route open. The same guard is applied at different levels: globally it would
	// go on router.New; here it guards the whole product GROUP (WithApp) and a single
	// HANDLER (the compact route), while widget and user build their own per-route
	// guards inside Routes.
	guard := auth.Guard(d.AuthVerifier(ctx), d.Opts.Auth.RequiredRole)

	// widget and user take only the seam — the uniform Routes signature. They build
	// their write guard from the injected verifier and attach it per route, so reads
	// stay public and writes are guarded (this is the per-route level).
	d.WidgetHandler(ctx).Routes(handle)
	d.UserHandler(ctx).Routes(handle)

	// product demonstrates the group level: the whole product group, including
	// reads, is wrapped once with the guard via WithApp (the typed-layer twin of
	// With), then handed that group's HandleApp. The feature adds no auth of its own.
	d.ProductHandler(ctx).Routes(api.WithApp(guard).HandleApp)

	// Audit-log read API (history / diff / changed-fields), mounted in one call.
	// Reads are public here; pass guard to restrict them to admins if needed.
	auditrest.NewHandlers(d.AuditLog(ctx)).Routes(handle)

	// Admin: trigger one compaction batch on demand — a write, guarded at the
	// single-HANDLER level. Scheduled compaction is wired separately via the worker
	// package (see app/server: a worker.Loop calling AuditLog.CompactBatch).
	handle("POST /auditlog/compact", compactHandler(d.AuditLog(ctx), d.Opts.Audit), guard)
}

// compactHandler builds the on-demand audit-log compaction endpoint, closing over
// the audit core and the configured compaction options.
func compactHandler(core *auditlog.Core, a config.Audit) rest.HandlerFunc {
	return func(ctx context.Context, _ *http.Request) rest.ResponseEncoder {
		res, err := core.CompactBatch(ctx, auditlog.CompactBatchOptions{
			Threshold: a.CompactThreshold,
			Limit:     a.CompactLimit,
			Compact: auditlog.CompactOptions{
				Factor:      a.Factor,
				KeepRecent:  a.KeepRecent,
				MaxVersions: a.MaxVersions,
			},
		})
		if err != nil {
			return errs.New(errs.Internal, err)
		}
		return rest.JSON(map[string]int{"models": res.Models, "deleted": res.Deleted})
	}
}

// errorOutcomeRecorder registers an errs-aware outcome counter on the metrics
// registry and returns the callback mid.Metrics reports each request's code
// to. Counts are labeled by domain code ("ok" for success), so /metrics exposes
// the error rate by code, alongside the HTTP-status request metrics.
func errorOutcomeRecorder(m *metrics.Metrics) func(code string) {
	outcomes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "skit",
		Subsystem: "app",
		Name:      "request_outcomes_total",
		Help:      "Request outcomes by domain error code (ok for success).",
	}, []string{"code"})
	m.Registry.MustRegister(outcomes)
	return func(code string) {
		outcomes.WithLabelValues(code).Inc()
	}
}
