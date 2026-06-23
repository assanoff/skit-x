// Package reqctx is the application's single "edge" request-context middleware:
// it parses the cross-cutting request headers once, stores them in the context
// as a RequestContext, and exposes typed getters. Other middleware and handlers
// read from here instead of re-parsing headers — e.g. the translation middleware
// takes the language via reqctx.Language, and error localization via the same.
//
// This is the application-level "parse once, read everywhere" pattern (cf.
// chocodev/stories sdk/mid). It deliberately lives in the app, not the servicekit
// SDK: the exact headers (X-Tenant-ID, X-City-ID, ...) are application-specific,
// whereas the SDK packages stay generic and own their own context values
// (auth.Principal, otel trace, i18n language) so they remain usable in isolation.
package reqctx

import (
	"context"
	"net/http"
	"strconv"

	"github.com/assanoff/servicekit/i18n"
	"github.com/assanoff/servicekit/web/rest"
)

// Request headers parsed at the edge.
const (
	TenantHeader = "X-Tenant-ID"
	CityHeader   = "X-City-ID"
	// Language is resolved by i18n.RequestLang (X-Language, then Accept-Language).
)

// RequestContext is the per-request scope data parsed from headers.
type RequestContext struct {
	Language string // normalized base language code, e.g. "ru", "kk"
	TenantID string
	CityID   int
}

type ctxKey struct{}

// Middleware parses the request headers once and stores the RequestContext in
// the context for downstream middleware and handlers. Install it as the
// outermost app middleware so everything else can read the resolved values.
func Middleware() rest.MidFunc {
	return func(next rest.HandlerFunc) rest.HandlerFunc {
		return func(ctx context.Context, r *http.Request) rest.Encoder {
			rc := RequestContext{
				Language: i18n.RequestLang(r),
				TenantID: r.Header.Get(TenantHeader),
				CityID:   headerInt(r, CityHeader),
			}
			return next(context.WithValue(ctx, ctxKey{}, rc), r)
		}
	}
}

// FromContext returns the RequestContext stored by Middleware (zero value if absent).
func FromContext(ctx context.Context) RequestContext {
	rc, _ := ctx.Value(ctxKey{}).(RequestContext)
	return rc
}

// Language returns the resolved request language code.
func Language(ctx context.Context) string { return FromContext(ctx).Language }

// TenantID returns the request tenant id.
func TenantID(ctx context.Context) string { return FromContext(ctx).TenantID }

// CityID returns the request city id.
func CityID(ctx context.Context) int { return FromContext(ctx).CityID }

// headerInt reads an integer header, returning 0 when absent or malformed.
func headerInt(r *http.Request, key string) int {
	n, err := strconv.Atoi(r.Header.Get(key))
	if err != nil {
		return 0
	}
	return n
}
