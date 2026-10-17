package server

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"

	"github.com/assanoff/skit/auditlog"
	"github.com/assanoff/skit/auditlog/auditrest"
	"github.com/assanoff/skit/auth"
	"github.com/assanoff/skit/errs"
	"github.com/assanoff/skit/httplog"
	"github.com/assanoff/skit/metrics"
	"github.com/assanoff/skit/middleware"
	"github.com/assanoff/skit/rest"
	"github.com/assanoff/skit/rest/mid"
	"github.com/assanoff/skit/rest/router"
	"github.com/assanoff/skit/translation/translationrest"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/reqctx"
)

// Install is the business API route set — the single place that composes the
// application's REST routes. It builds the business group and delegates each
// feature's route registration to its Routes(handle, ...) method via the
// router's typed registration seam (router.HandleApp, a rest.Handle). Feature
// handlers therefore never depend on the router type — Install owns route
// composition (grouping, middleware), the feature owns its endpoints.
//
// The group carries the whole request chain: the transport middleware (server
// span, trace context, access log, HTTP metrics, per-request timeout, body-size
// limit) and the
// application middleware (appMids). It wraps these routes only, so the technical
// sets mounted next to them stay out of the access log, the HTTP metrics and the
// traces, and a long pprof profile is not cut off by the request timeout. Only
// processes whose server lists Install resolve the handlers, the translators and
// the cores behind them from the container.
func Install(ctx context.Context, a *App, r *router.Router) {
	d := a.d
	api := r.With(
		// otelhttp opens the server span of every business request, named after
		// the route ("GET /widgets/jsonapi/{id}"), and records its status. The span
		// is otelhttp's: nothing below it ends the span.
		otelhttp.NewMiddleware("http.server", otelhttp.WithSpanNameFormatter(spanName)),
		// TraceRequest continues that span — it does not re-extract the headers
		// over it — stores the tracer for otel.AddSpan, puts the trace_id the
		// access log carries into the context and returns the span's
		// traceparent with the response.
		middleware.TraceRequest(d.Tracer(ctx)),
		// Access logging via the vendored httplog (tagged logger=access).
		// RecoverPanics turns a panic below the logger into a 500 with the
		// panic as its error, so the access log and the server span record it;
		// the root recoverer (see handler) only sees what escapes this chain.
		httplog.RequestLogger(d.Logger.With("logger", "access"), &httplog.Options{
			Level:         slog.LevelInfo,
			Schema:        httplog.SchemaOTEL.Concise(true),
			RecoverPanics: true,
		}),
		a.m.Middleware(),
		middleware.Timeout(d.Opts.HTTP.RequestTimeout),
		middleware.SizeLimit(d.Opts.HTTP.BodySizeLimit),
	).WithApp(a.appMids(ctx)...)
	// handle is the registration seam handed to every feature: it inherits the
	// group's transport and application middleware.
	handle := api.HandleApp

	// Authorization is a single guard middleware: auth.Guard composes Authenticate
	// + RequireRole (via rest.Chain) into one rest.MidFunc. d.AuthVerifier is nil
	// when auth is disabled and auth.Guard then returns nil — a no-op the Handle
	// seam skips — so no Auth.Enabled check is needed here: a nil guard leaves every
	// route open. The same guard is applied at different levels: globally it would
	// go on appMids; here it guards the whole product GROUP (WithApp) and a single
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

// spanName names the server span after the matched route, so traces group by
// endpoint rather than by a raw path with ids in it.
func spanName(operation string, r *http.Request) string {
	return cmp.Or(r.Pattern, operation)
}

// appMids returns the application middleware every business route runs,
// outermost first. It is built once, on the first Install — so Install can go
// on more than one server, and a process without it never resolves the
// translators:
//   - reqctx parses the cross-cutting request headers ONCE (language, tenant,
//     city) into the context; everything below reads from there instead of
//     re-parsing headers.
//   - mid.Chain bundles, in order: Metrics (errs-aware outcome counter) ->
//     LocalizeErrors (localizes any *errs.Error into the request language
//     resolved by reqctx.Language) -> MaskInternal (hides 5xx detail from
//     clients in production) -> Errors (records 5xx on the trace span + logs
//     them) -> Panics (turns a handler panic into a clean Internal error).
//   - translationrest translates per-record content: when a handler returns a
//     translation.Translatable (or TranslatableList) it applies the stored
//     translation in place — so widget read handlers stay translation-agnostic.
//
// Chain is the SDK-standard core; reqctx and translation are app-specific and
// wrap it. Audit recording is NOT a transport concern here — the widget domain
// emits audit events on the eventbus (see core/widget), which covers REST, gRPC
// and background paths uniformly.
func (a *App) appMids(ctx context.Context) []rest.MidFunc {
	if a.mids != nil {
		return a.mids
	}
	d := a.d
	a.mids = make([]rest.MidFunc, 0, 3)
	a.mids = append(a.mids, reqctx.Middleware())
	a.mids = append(a.mids, mid.Chain(mid.Config{
		Translator:   d.Translator(ctx),
		Lang:         reqctx.Language,
		Logger:       d.Logger,
		MaskInternal: d.Opts.Env == "production",
		RecordMetric: errorOutcomeRecorder(a.m),
	})...)
	a.mids = append(a.mids, translationrest.MiddlewareWithLang(d.Logger, d.Translation(ctx), reqctx.Language))
	return a.mids
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
