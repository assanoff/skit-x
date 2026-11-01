package tests

import (
	"context"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/assanoff/skit/auditlog/auditpg"
	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/dbx/pg"
	"github.com/assanoff/skit/queue"
	"github.com/assanoff/skit/translation/translationpg"

	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widget/widgetdb"
	"github.com/assanoff/skit-x/internal/app/config"
)

// ensureSDKSchemas provisions the tables owned by the SDK storage packages
// (queue/auditlog/translation), which the app no longer keeps in its
// migrations — mirroring what the deps providers do at app startup. startPostgres
// calls it so every integration test has these tables regardless of how it later
// opens its own DB. The advisory-lock-guarded EnsureSchema is idempotent, so the
// duplicate calls across tests (and against the deps providers) are harmless.
func ensureSDKSchemas(ctx context.Context, t *testing.T, cfg config.ServerOpts) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	db := openTestDB(t, cfg)
	must := func(name string, err error) {
		if err != nil {
			t.Fatalf("ensure %s schema: %v", name, err)
		}
	}
	must("queue", queue.NewPG(log, db).EnsureSchema(ctx))
	must("auditlog", auditpg.NewStore(log, db).EnsureSchema(ctx))
	must("translation", translationpg.NewStore(log, db).EnsureSchema(ctx))
}

// openTestDB opens a pool against the test database. Shared by the integration
// tests.
func openTestDB(t *testing.T, cfg config.ServerOpts) *dbx.DB {
	t.Helper()
	return openTracedDB(t, cfg, nil)
}

// openTracedDB opens the test database with its queries recorded on tp.
func openTracedDB(t *testing.T, cfg config.ServerOpts, tp trace.TracerProvider) *dbx.DB {
	t.Helper()
	db, err := pg.Open(pg.Config{
		User: cfg.DB.User, Password: cfg.DB.Password, Host: cfg.DB.Host,
		Name: cfg.DB.Name, Schema: cfg.DB.Schema,
		DisableTLS: true, MaxIdleConns: 4, MaxOpenConns: 10,
		TracerProvider: tp,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// widgetStore builds the widget store as the app does with the broker off:
// widget.created is routed with queue.Drop, so Create writes no event.
func widgetStore(t *testing.T, log *slog.Logger, db *dbx.DB) *widgetdb.Store {
	t.Helper()
	q := queue.NewPG(log, db)
	reg := queue.NewRegistry()
	err := reg.Register[widget.Created]("events", widget.EventWidgetCreated, queue.Drop())
	if err != nil {
		t.Fatalf("route widget.created: %v", err)
	}
	tasks := func(tx *dbx.Tx) *queue.Scheduler {
		return queue.Bind(tx, q, reg)
	}
	return widgetdb.NewStore(log, db, widgetdb.WithTasks(tasks))
}
