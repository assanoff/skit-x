package tests

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/poller"

	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widget/widgetdb"
)

// TestWidgetCountPoller exercises the poller demo: a poller caches the widget
// count and refreshes it on a short interval. After inserting widgets the cached
// value (read via Current, never touching the database) catches up.
func TestWidgetCountPoller(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	cfg := startPostgres(ctx, t)
	db := openTestDB(t, cfg)
	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})

	store := widgetdb.NewStore(log, db)
	core := widget.NewCore(log, store)

	// Count takes a filter; the poller caches the unfiltered total, so it passes
	// the zero QueryFilter (matches every widget).
	countAll := func(ctx context.Context) (int, error) {
		return core.Count(ctx, widget.QueryFilter{})
	}
	p := poller.New(log.Slog(), 0, countAll, poller.Config{
		Name:        "widget-count-test",
		Interval:    100 * time.Millisecond,
		PollTimeout: 2 * time.Second,
	})

	if got := p.Current(); got != 0 {
		t.Fatalf("seeded count = %d, want 0", got)
	}

	for _, n := range []string{"a", "b", "c"} {
		if _, err := core.Create(ctx, widget.NewWidget{Name: n}); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}

	// Supervise the poller; it polls immediately on Start and then every interval.
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = p.Start(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.After(5 * time.Second)
	for p.Current() != 3 {
		select {
		case <-deadline:
			t.Fatalf("poller count = %d, want 3", p.Current())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
