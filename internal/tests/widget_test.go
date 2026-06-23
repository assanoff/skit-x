// Package tests holds integration tests that exercise the full HTTP stack
// against a real Postgres started via testcontainers. Run with:
//
//	go test ./internal/tests/...
//
// They are skipped under `go test -short`.
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assanoff/servicekit/dbtest"
	"github.com/assanoff/servicekit/logger"

	"github.com/assanoff/service-kit-x/internal/app/config"
	"github.com/assanoff/service-kit-x/internal/app/server"
	"github.com/assanoff/service-kit-x/internal/migrations"
)

func TestWidgetCRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	// Create.
	created := mustDo(t, srv, http.MethodPost, "/widgets", `{"name":"gadget","description":"shiny"}`, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("expected an id, got %v", created)
	}
	if created["name"] != "gadget" {
		t.Fatalf("name mismatch: %v", created)
	}

	// Get by id.
	got := mustDo(t, srv, http.MethodGet, "/widgets/"+id, "", http.StatusOK)
	if got["description"] != "shiny" {
		t.Fatalf("description mismatch: %v", got)
	}

	// List.
	listResp := doReq(t, srv, http.MethodGet, "/widgets", "")
	assertStatus(t, listResp, http.StatusOK)
	var list []map[string]any
	decode(t, listResp, &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 widget, got %d", len(list))
	}

	// Update.
	updated := mustDo(t, srv, http.MethodPut, "/widgets/"+id, `{"description":"updated"}`, http.StatusOK)
	if updated["description"] != "updated" {
		t.Fatalf("update did not apply: %v", updated)
	}
	if updated["name"] != "gadget" {
		t.Fatalf("update should preserve name: %v", updated)
	}

	// Validation error: empty name -> 400 with code.
	bad := doReq(t, srv, http.MethodPost, "/widgets", `{"description":"x"}`)
	assertStatus(t, bad, http.StatusBadRequest)
	var errBody map[string]any
	decode(t, bad, &errBody)
	if errBody["code"] != "invalid_argument" {
		t.Fatalf("expected invalid_argument, got %v", errBody)
	}

	// Delete -> 204, then 404.
	delResp := doReq(t, srv, http.MethodDelete, "/widgets/"+id, "")
	assertStatus(t, delResp, http.StatusNoContent)

	missing := doReq(t, srv, http.MethodGet, "/widgets/"+id, "")
	assertStatus(t, missing, http.StatusNotFound)

	// Health endpoints.
	assertStatus(t, doReq(t, srv, http.MethodGet, "/healthz", ""), http.StatusOK)
	assertStatus(t, doReq(t, srv, http.MethodGet, "/readyz", ""), http.StatusOK)
}

// newTestServer starts Postgres, runs migrations, and returns an httptest server
// driving the real application handler.
func newTestServer(ctx context.Context, t *testing.T) (*httptest.Server, config.ServerOpts) {
	t.Helper()
	cfg := startPostgres(ctx, t)

	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})
	slog.SetDefault(log.Slog()) // quiet dim/closer init logs

	handler, err := server.Handler(ctx, cfg, log)
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, cfg
}

// startPostgres launches a Postgres container, runs migrations via the SDK's
// dbtest helper, and returns an app config pointing at it. Shared by the REST
// and gRPC integration tests.
func startPostgres(ctx context.Context, t *testing.T) config.ServerOpts {
	t.Helper()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Config{Migrations: migrations.FS})

	cfg := config.ServerOpts{Service: "test"}
	cfg.DB = config.DB{
		User:         pg.Config.User,
		Password:     pg.Config.Password,
		Host:         pg.Config.Host,
		Name:         pg.Config.Name,
		Schema:       pg.Config.Schema,
		MaxIdleConns: pg.Config.MaxIdleConns,
		MaxOpenConns: pg.Config.MaxOpenConns,
		DisableTLS:   pg.Config.DisableTLS,
	}
	cfg.HTTP.RequestTimeout = 10 * time.Second
	return cfg
}

// --- HTTP helpers ---

func doReq(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func mustDo(t *testing.T, srv *httptest.Server, method, path, body string, wantStatus int) map[string]any {
	t.Helper()
	resp := doReq(t, srv, method, path, body)
	assertStatus(t, resp, wantStatus)
	var out map[string]any
	decode(t, resp, &out)
	return out
}

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("status: got %d want %d (body: %s)", resp.StatusCode, want, b)
	}
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}
