package tests

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/matryer/is"

	"github.com/assanoff/servicekit/logger"

	"github.com/assanoff/service-kit-x/internal/app/server"
)

// TestProductCRUD exercises the product module end to end (auth disabled by
// default, so the group is open).
func TestProductCRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	// Create.
	resp := doReq(t, srv, http.MethodPost, "/products", `{"name":"Keyboard","price":4999}`)
	is.Equal(resp.StatusCode, http.StatusCreated)
	var created map[string]any
	decode(t, resp, &created)
	id, _ := created["id"].(string)
	is.True(id != "")                         // id assigned
	is.Equal(created["price"], float64(4999)) // price echoed (JSON number)

	// Get by id.
	resp = doReq(t, srv, http.MethodGet, "/products/"+id, "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var got map[string]any
	decode(t, resp, &got)
	is.Equal(got["name"], "Keyboard")

	// Update (partial: price only) preserves name.
	resp = doReq(t, srv, http.MethodPut, "/products/"+id, `{"price":3999}`)
	is.Equal(resp.StatusCode, http.StatusOK)
	var updated map[string]any
	decode(t, resp, &updated)
	is.Equal(updated["price"], float64(3999)) // update applied
	is.Equal(updated["name"], "Keyboard")     // name preserved

	// Delete -> 204, then 404.
	resp = doReq(t, srv, http.MethodDelete, "/products/"+id, "")
	is.Equal(resp.StatusCode, http.StatusNoContent)
	_ = resp.Body.Close()

	resp = doReq(t, srv, http.MethodGet, "/products/"+id, "")
	is.Equal(resp.StatusCode, http.StatusNotFound)
	_ = resp.Body.Close()
}

// TestGroupVsRouteAuth contrasts the two authorization patterns under one server
// with auth enabled: product is guarded as a whole group (api.WithApp), so even
// reads need a token; user guards only writes (per-route), so user reads stay
// public.
func TestGroupVsRouteAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	cfg := startPostgres(ctx, t)
	cfg.Auth.Enabled = true
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Auth.RequiredRole = "admin"
	cfg.HTTP.RequestTimeout = 10 * time.Second

	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})
	slog.SetDefault(log.Slog())
	handler, err := server.Handler(ctx, cfg, log)
	is.NoErr(err) // build handler
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// product group is fully guarded (WithApp): a read without a token -> 401.
	resp := doReq(t, srv, http.MethodGet, "/products", "")
	is.Equal(resp.StatusCode, http.StatusUnauthorized) // product reads guarded
	_ = resp.Body.Close()

	// user reads stay public even with auth enabled (per-route auth on writes only).
	resp = doReq(t, srv, http.MethodGet, "/users", "")
	is.Equal(resp.StatusCode, http.StatusOK) // user reads public
	_ = resp.Body.Close()

	// user writes are guarded -> 401 without a token.
	resp = doReq(t, srv, http.MethodPost, "/users", `{"email":"bob@example.com","name":"Bob"}`)
	is.Equal(resp.StatusCode, http.StatusUnauthorized) // user writes guarded
	_ = resp.Body.Close()

	// With a valid admin token, the product read succeeds.
	token := signToken(t, "test-secret", "admin")
	resp = getWithToken(t, srv, "/products", token)
	is.Equal(resp.StatusCode, http.StatusOK) // authorized product read
	_ = resp.Body.Close()
}

// getWithToken issues a GET carrying a bearer token.
func getWithToken(t *testing.T, srv *httptest.Server, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}
