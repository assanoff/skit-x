package tests

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matryer/is"

	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/settings"

	"github.com/assanoff/skit-x/internal/app/server"
)

// An admin saves the widget settings, and the API obeys them at once:
// read-only pauses the writes, the import limit caps a batch. A stored
// document without a field keeps the default of that field.
func TestWidgetSettings(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)
	ctx := context.Background()
	cfg := startPostgres(ctx, t)

	// A document saved before import_limit was a setting.
	db := openTestDB(t, cfg)
	is.NoErr(settings.EnsureSchema(ctx, nil, db))
	const old = `INSERT INTO settings (name, value) VALUES ('widgets', '{"read_only": true}')`
	is.NoErr(dbx.Exec(ctx, db, old))

	log := slog.New(slog.DiscardHandler)
	slog.SetDefault(log) // quiet dim/closer init logs
	handler, err := server.Handler(ctx, cfg, log)
	is.NoErr(err)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	got := mustDo(t, srv, http.MethodGet, "/admin/widget-settings", "", http.StatusOK)
	is.Equal(got["read_only"], true)
	is.Equal(got["import_limit"], 1000.0) // the default
	mustDo(t, srv, http.MethodPost, "/widgets", `{"name":"a"}`, http.StatusServiceUnavailable)

	const limit2 = `{"read_only":false,"import_limit":2}`
	got = mustDo(t, srv, http.MethodPut, "/admin/widget-settings", limit2, http.StatusOK)
	is.Equal(got["import_limit"], 2.0)
	mustDo(t, srv, http.MethodPost, "/widgets", `{"name":"a"}`, http.StatusCreated)

	three := `{"widgets":[{"name":"b"},{"name":"c"},{"name":"d"}]}`
	mustDo(t, srv, http.MethodPost, "/widgets/import", three, http.StatusBadRequest)
	two := `{"widgets":[{"name":"b"},{"name":"c"}]}`
	mustDo(t, srv, http.MethodPost, "/widgets/import", two, http.StatusAccepted)

	const noLimit = `{"read_only":true}`
	mustDo(t, srv, http.MethodPut, "/admin/widget-settings", noLimit, http.StatusBadRequest)
	got = mustDo(t, srv, http.MethodGet, "/admin/widget-settings", "", http.StatusOK)
	is.Equal(got["read_only"], false) // a rejected document is not saved
}
