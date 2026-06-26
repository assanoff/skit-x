package tests

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/worker"

	"github.com/assanoff/skit-x/internal/app/config"
)

// TestOutboxStoreFSM drives the outbox Store through its full FSM against a real
// Postgres: insert -> lease -> mark sent, lease guard, retry vs terminal failure,
// lease sweep, and cleanup.
func TestOutboxStoreFSM(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	cfg := startPostgres(ctx, t)
	db := openTestDB(t, cfg)
	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})

	// Near-zero backoff so a retryable failure is immediately re-leasable.
	store := outbox.NewPG(log, db, outbox.Options{
		Backoff: worker.Backoff{Base: time.Millisecond, Factor: 1, Max: time.Millisecond},
	})

	t.Run("insert lease and mark sent", func(t *testing.T) {
		ev := mustNewEvent(t, "test.created")
		if err := store.Insert(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}

		now := time.Now().UTC()
		leased, err := store.LeasePending(ctx, now, 10)
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		got := findEvent(leased, ev.ID)
		if got == nil {
			t.Fatalf("inserted event not leased")
		}
		if got.Status != outbox.StatusInFlight || got.LeaseID == uuid.Nil {
			t.Fatalf("leased event not in_flight with a lease: %+v", got)
		}

		// A second lease must not see the in_flight row again.
		again, _ := store.LeasePending(ctx, now, 10)
		if findEvent(again, ev.ID) != nil {
			t.Error("in_flight event was leased twice")
		}

		if err := store.MarkSent(ctx, *got, got.LeaseID, now); err != nil {
			t.Fatalf("mark sent: %v", err)
		}
		if s := statusOf(t, db, ev.ID); s != outbox.StatusSent {
			t.Errorf("status = %q, want sent", s)
		}

		// Marking again with a stale lease is ErrLeaseLost (row no longer in_flight).
		if err := store.MarkSent(ctx, *got, got.LeaseID, now); !errors.Is(err, outbox.ErrLeaseLost) {
			t.Errorf("expected ErrLeaseLost on re-mark, got %v", err)
		}
	})

	t.Run("retry then terminal failure", func(t *testing.T) {
		ev := mustNewEvent(t, "test.fails")
		ev.MaxAttempts = 2
		if err := store.Insert(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}

		// Attempt 1: retryable -> back to pending.
		now := time.Now().UTC()
		leased, _ := store.LeasePending(ctx, now, 10)
		g := findEvent(leased, ev.ID)
		if g == nil {
			t.Fatal("event not leased on attempt 1")
		}
		if err := store.MarkFailed(ctx, *g, g.LeaseID, "boom", now); err != nil {
			t.Fatalf("mark failed 1: %v", err)
		}
		if s := statusOf(t, db, ev.ID); s != outbox.StatusPending {
			t.Fatalf("after attempt 1 status = %q, want pending", s)
		}

		// Attempt 2: hits max_attempts -> terminal failed.
		now = time.Now().UTC().Add(time.Second)
		leased, _ = store.LeasePending(ctx, now, 10)
		g = findEvent(leased, ev.ID)
		if g == nil {
			t.Fatal("event not re-leased on attempt 2")
		}
		if err := store.MarkFailed(ctx, *g, g.LeaseID, "boom again", now); err != nil {
			t.Fatalf("mark failed 2: %v", err)
		}
		if s := statusOf(t, db, ev.ID); s != outbox.StatusFailed {
			t.Errorf("after attempt 2 status = %q, want failed", s)
		}
	})

	t.Run("sweep reclaims expired lease", func(t *testing.T) {
		ev := mustNewEvent(t, "test.sweep")
		if err := store.Insert(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}
		leaseTime := time.Now().UTC()
		if _, err := store.LeasePending(ctx, leaseTime, 10); err != nil {
			t.Fatalf("lease: %v", err)
		}

		// Sweep well after the lease, with a short timeout, reclaims it.
		n, err := store.SweepExpiredLeases(ctx, 500*time.Millisecond, leaseTime.Add(2*time.Second), 10)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if n < 1 {
			t.Fatalf("expected to reclaim >=1, got %d", n)
		}
		if s := statusOf(t, db, ev.ID); s != outbox.StatusPending {
			t.Errorf("after sweep status = %q, want pending", s)
		}
	})

	t.Run("cleanup deletes terminal rows", func(t *testing.T) {
		ev := mustNewEvent(t, "test.cleanup")
		if err := store.Insert(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}
		now := time.Now().UTC()
		leased, _ := store.LeasePending(ctx, now, 10)
		g := findEvent(leased, ev.ID)
		if err := store.MarkSent(ctx, *g, g.LeaseID, now); err != nil {
			t.Fatalf("mark sent: %v", err)
		}

		// Retention 0 with a future clock deletes all sent/failed rows.
		deleted, err := store.Cleanup(ctx, 0, time.Now().UTC().Add(time.Hour))
		if err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		if deleted < 1 {
			t.Errorf("expected to delete >=1 terminal row, got %d", deleted)
		}
	})

	t.Run("stats reports backlog", func(t *testing.T) {
		// A fresh pending event with a created_at in the past so OldestPendingAge
		// is measurable (the stats query exercises FILTER + EXTRACT(EPOCH ...)).
		ev := mustNewEvent(t, "test.stats")
		ev.CreatedAt = time.Now().UTC().Add(-30 * time.Second)
		ev.NextAttemptAt = ev.CreatedAt
		if err := store.Insert(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}

		st, err := store.Stats(ctx, time.Now().UTC())
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if st.Pending < 1 {
			t.Errorf("pending = %d, want >=1", st.Pending)
		}
		if st.OldestPendingAge < 20*time.Second {
			t.Errorf("oldest pending age = %s, want >=20s", st.OldestPendingAge)
		}
	})
}

func mustNewEvent(t *testing.T, typ string) outbox.Event {
	t.Helper()
	ev, err := outbox.NewEvent(typ, "test-topic", "test.key", "application/json", []byte(`{"v":1}`), nil)
	if err != nil {
		t.Fatalf("new event: %v", err)
	}
	return ev
}

func findEvent(evs []outbox.Event, id uuid.UUID) *outbox.Event {
	for i := range evs {
		if evs[i].ID == id {
			return &evs[i]
		}
	}
	return nil
}

func statusOf(t *testing.T, db *sqlx.DB, id uuid.UUID) string {
	t.Helper()
	var status string
	if err := db.GetContext(context.Background(), &status,
		`SELECT status FROM outbox_events WHERE id = $1`, id); err != nil {
		t.Fatalf("query status: %v", err)
	}
	return status
}

// openTestDB opens a pool against the test database. Shared by the outbox and
// broker integration tests.
func openTestDB(t *testing.T, cfg config.ServerOpts) *sqlx.DB {
	t.Helper()
	db, err := dbx.Open(dbx.Config{
		User: cfg.DB.User, Password: cfg.DB.Password, Host: cfg.DB.Host,
		Name: cfg.DB.Name, Schema: cfg.DB.Schema,
		DisableTLS: true, MaxIdleConns: 4, MaxOpenConns: 10,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
