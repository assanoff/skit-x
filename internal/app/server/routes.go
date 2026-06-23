package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/assanoff/servicekit/auditlog"
	"github.com/assanoff/servicekit/auditlog/auditrest"
	"github.com/assanoff/servicekit/auth"
	"github.com/assanoff/servicekit/debugsrv"
	"github.com/assanoff/servicekit/errs"
	"github.com/assanoff/servicekit/health"
	"github.com/assanoff/servicekit/httplog"
	"github.com/assanoff/servicekit/i18n"
	"github.com/assanoff/servicekit/metrics"
	"github.com/assanoff/servicekit/translation/translationrest"
	"github.com/assanoff/servicekit/web/mid"
	"github.com/assanoff/servicekit/web/rest"
	"github.com/assanoff/servicekit/web/router"

	"github.com/assanoff/service-kit-x/internal/app/deps"
	"github.com/assanoff/service-kit-x/internal/app/reqctx"
)

// buildRouter constructs the HTTP handler. Cross-cutting middleware
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
	//   - localizeErrors localizes any *errs.Error response into reqctx.Language.
	//   - translationrest translates per-record content: when a handler returns a
	//     translation.Translatable (or TranslatableList) it applies the stored
	//     translation in place — so widget read handlers stay translation-agnostic.
	// Audit recording is NOT a transport concern here — the widget domain emits
	// audit events on the eventbus (see core/widget), which covers REST, gRPC and
	// background paths uniformly.
	r := router.New(
		reqctx.Middleware(),
		localizeErrors(translator),
		translationrest.MiddlewareWithLang(log, d.Translation(ctx), reqctx.Language),
	)

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
		mid.TraceRequest(tracer),
		httplog.Middleware(log.Named("access"), httpLogOpts),
	)

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
	// request timeout, body-size limit). Widget writes are protected with JWT
	// auth + RBAC when auth is enabled; reads stay public.
	api := r.With(
		m.Middleware(),
		mid.Timeout(d.Opts.HTTP.RequestTimeout),
		mid.SizeLimit(d.Opts.HTTP.BodySizeLimit),
	)

	var authMW []router.Middleware
	if d.Opts.Auth.Enabled {
		authMW = []router.Middleware{
			router.Middleware(auth.Authenticate(d.Verifier(ctx), log)),
			router.Middleware(auth.RequireRole(d.Opts.Auth.RequiredRole)),
		}
	}
	d.WidgetHandler(ctx).Routes(api, authMW...)

	// Audit-log read API (history / diff / changed-fields), mounted in one call.
	// Reads are public here; pass authMW to restrict them to admins if needed.
	auditrest.NewHandlers(d.AuditLog(ctx)).Routes(api)

	// Admin: trigger one compaction batch on demand. Protected by authMW when auth
	// is enabled. Scheduled compaction is wired separately via the worker package
	// (see app/server: a worker.Loop calling AuditLog.CompactBatch).
	adminAudit := api
	if len(authMW) > 0 {
		adminAudit = api.With(authMW...)
	}
	a := d.Opts.Audit
	adminAudit.HandleApp("POST /auditlog/compact", func(ctx context.Context, _ *http.Request) rest.Encoder {
		res, err := d.AuditLog(ctx).CompactBatch(ctx, auditlog.CompactBatchOptions{
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
	})

	return r
}

// localizeErrors returns an app middleware that translates *errs.Error responses
// into the request language (resolved once by reqctx) before they are encoded.
func localizeErrors(tr *i18n.Translator) rest.MidFunc {
	return func(next rest.HandlerFunc) rest.HandlerFunc {
		return func(ctx context.Context, r *http.Request) rest.Encoder {
			resp := next(ctx, r)
			if e, ok := resp.(*errs.Error); ok {
				return tr.TranslateError(reqctx.Language(ctx), e)
			}
			return resp
		}
	}
}
