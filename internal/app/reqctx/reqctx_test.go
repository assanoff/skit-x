package reqctx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assanoff/skit/rest"

	"github.com/assanoff/skit-x/internal/app/reqctx"
)

func TestMiddlewareParsesHeaders(t *testing.T) {
	var got reqctx.RequestContext
	h := reqctx.Middleware()(func(ctx context.Context, _ *http.Request) rest.ResponseEncoder {
		got = reqctx.FromContext(ctx)
		return nil
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Language", "kk-KZ") // normalized to base "kk"
	r.Header.Set("X-Tenant-ID", "acme")
	r.Header.Set("X-City-ID", "7")

	_ = h(r.Context(), r)

	if got.Language != "kk" {
		t.Errorf("Language = %q, want kk", got.Language)
	}
	if got.TenantID != "acme" {
		t.Errorf("TenantID = %q, want acme", got.TenantID)
	}
	if got.CityID != 7 {
		t.Errorf("CityID = %d, want 7", got.CityID)
	}
}

func TestGettersAndEmpty(t *testing.T) {
	// Getters on a context without reqctx return zero values.
	ctx := context.Background()
	if reqctx.Language(ctx) != "" || reqctx.TenantID(ctx) != "" || reqctx.CityID(ctx) != 0 {
		t.Fatal("expected zero values from an empty context")
	}

	// A malformed integer header parses to 0.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-City-ID", "not-a-number")
	h := reqctx.Middleware()(func(ctx context.Context, _ *http.Request) rest.ResponseEncoder {
		if reqctx.CityID(ctx) != 0 {
			t.Errorf("malformed CityID = %d, want 0", reqctx.CityID(ctx))
		}
		return nil
	})
	_ = h(r.Context(), r)
}
