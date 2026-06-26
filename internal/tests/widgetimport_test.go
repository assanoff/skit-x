package tests

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/page"
	"github.com/assanoff/skit/queue"
	"github.com/assanoff/skit/worker"

	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widget/widgetdb"
	"github.com/assanoff/skit-x/core/widgetimport"
)

// TestWidgetImportEndpoint exercises the REST -> queue path: POST /widgets/import
// enqueues a batch (202 Accepted) and a repeated Name dedups to a no-op.
func TestWidgetImportEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	srv, cfg := newTestServer(ctx, t)

	body := `{"name":"batch-a","widgets":[{"name":"ep-1"},{"name":"ep-2","description":"two"}]}`
	resp := mustDo(t, srv, http.MethodPost, "/widgets/import", body, http.StatusAccepted)
	if resp["scheduled"] != true {
		t.Fatalf("expected scheduled=true, got %v", resp)
	}
	if resp["count"] != float64(2) {
		t.Fatalf("expected count=2, got %v", resp["count"])
	}

	// Re-posting the same dedup name is accepted but not re-enqueued.
	dup := mustDo(t, srv, http.MethodPost, "/widgets/import", body, http.StatusAccepted)
	if dup["scheduled"] != false {
		t.Errorf("expected dedup scheduled=false, got %v", dup)
	}

	// Empty batch fails validation.
	bad := doReq(t, srv, http.MethodPost, "/widgets/import", `{"widgets":[]}`)
	assertStatus(t, bad, http.StatusBadRequest)

	// Exactly one task is sitting in the queue (Handler builds deps but does not
	// run the worker, so nothing drains it here).
	db, err := dbx.Open(dbx.Config{
		User: cfg.DB.User, Password: cfg.DB.Password, Host: cfg.DB.Host,
		Name: cfg.DB.Name, Schema: cfg.DB.Schema, DisableTLS: true,
		MaxIdleConns: 2, MaxOpenConns: 5,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var n int
	if err := db.GetContext(ctx, &n, `SELECT count(*) FROM queue_tasks`); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 enqueued task, got %d", n)
	}
}

// TestWidgetImportQueue exercises the reliable-processing stack end to end:
// import batches are enqueued on the durable queue and drained by TWO concurrent
// processors. Because the queue claims with FOR UPDATE SKIP LOCKED, the two
// workers never grab the same task, so every widget lands exactly once and the
// queue fully drains.
func TestWidgetImportQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	cfg := startPostgres(ctx, t)

	db, err := dbx.Open(dbx.Config{
		User: cfg.DB.User, Password: cfg.DB.Password, Host: cfg.DB.Host,
		Name: cfg.DB.Name, Schema: cfg.DB.Schema, DisableTLS: true,
		MaxIdleConns: 4, MaxOpenConns: 10,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})
	store := widgetdb.NewStore(log, db)
	q := queue.NewPG(log, db, queue.Options{})
	im := widgetimport.New(log, q, store)

	// Enqueue 20 batches of 5 widgets each = 100 widgets.
	const batches, perBatch = 20, 5
	for b := range batches {
		ws := make([]widget.NewWidget, perBatch)
		for i := range ws {
			ws[i] = widget.NewWidget{Name: fmt.Sprintf("imported-%d-%d", b, i)}
		}
		if _, err := im.Schedule(ctx, fmt.Sprintf("batch-%d", b), ws); err != nil {
			t.Fatalf("schedule batch %d: %v", b, err)
		}
	}

	// Two concurrent processors over the same queue (small batch size so they
	// genuinely interleave). Supervised by a worker.Group.
	runCtx, cancel := context.WithCancel(ctx)
	group := worker.NewGroup(log.Slog(), 2*time.Second)
	group.Add(im.NewLoop(widgetimport.Config{Interval: 50 * time.Millisecond, BatchSize: 3}))
	group.Add(im.NewLoop(widgetimport.Config{Interval: 50 * time.Millisecond, BatchSize: 3}))

	groupDone := make(chan error, 1)
	go func() { groupDone <- group.Run(runCtx) }()

	// Wait until all 100 widgets have been inserted (or time out).
	const want = batches * perBatch
	deadline := time.After(20 * time.Second)
	for {
		ws, err := store.Query(ctx, widget.QueryFilter{}, widget.DefaultOrder, page.New(1, want))
		if err != nil {
			t.Fatalf("query widgets: %v", err)
		}
		if len(ws) >= want {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out: only %d/%d widgets imported", len(ws), want)
		case <-time.After(100 * time.Millisecond):
		}
	}

	cancel()
	<-groupDone

	// The queue must be fully drained: every task was acknowledged (deleted).
	var remaining int
	if err := db.GetContext(ctx, &remaining, `SELECT count(*) FROM queue_tasks`); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if remaining != 0 {
		t.Errorf("expected queue drained, %d tasks remain", remaining)
	}

	// Exactly the imported widgets, no duplicates (unique ids + ON CONFLICT).
	ws, _ := store.Query(ctx, widget.QueryFilter{}, widget.DefaultOrder, page.New(1, want))
	if len(ws) != want {
		t.Errorf("expected %d widgets, got %d", want, len(ws))
	}
}
