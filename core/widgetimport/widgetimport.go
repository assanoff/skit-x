// Package widgetimport is an example business module showing reliable background
// processing with skit: callers enqueue batch-import jobs onto a durable
// queue, and the workers of the queue drain it, bulk-inserting each batch.
//
// It ties together two SDK pieces:
//   - queue: durable, SKIP LOCKED work queue (safe across replicas) and its
//     workers, the claim -> handle -> ack/retry loop;
//   - dbx.BulkInsert: efficient multi-row writes (via widget's store).
//
// Delivery is at-least-once, so Handle must be idempotent — the store's
// BulkInsert uses ON CONFLICT DO NOTHING to make replays harmless.
package widgetimport

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/assanoff/skit/queue"
	"github.com/assanoff/skit/worker"

	"github.com/assanoff/skit-x/core/widget"
)

const (
	// Queue is the queue of the import tasks.
	Queue = "widget-import"
	// Kind is the queue task kind handled by this module.
	Kind = "widget-import"
)

// ErrBadPayload marks a permanently undecodable task payload. It comes marked
// queue.Permanent, so the task is dead-lettered instead of retried forever.
var ErrBadPayload = errors.New("widgetimport: bad payload")

// Store is the persistence contract: a batched, idempotent widget writer.
// Satisfied by widgetdb.Store.
type Store interface {
	BulkInsert(ctx context.Context, ws []widget.Widget) error
}

// Importer enqueues and processes widget-import batches.
type Importer struct {
	log   *slog.Logger
	q     *queue.PG
	store Store
}

// New builds an Importer over the given durable queue and store.
func New(log *slog.Logger, q *queue.PG, store Store) *Importer {
	return &Importer{log: log, q: q, store: store}
}

// Schedule enqueues a batch of widgets for import under the dedup key name
// (pass "" for a one-off). It returns inserted=false when a task with that key
// is already in the queue.
// IDs and timestamps are assigned here, at enqueue time, so replaying the task
// (at-least-once delivery) inserts the same rows — keeping Handle idempotent.
func (im *Importer) Schedule(ctx context.Context, name string, news []widget.NewWidget) (bool, error) {
	now := time.Now().UTC()
	ws := make([]widget.Widget, len(news))
	for i, n := range news {
		ws[i] = widget.Widget{
			ID:          uuid.New(),
			Name:        n.Name,
			Description: n.Description,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
	}
	payload, err := json.Marshal(ws, json.Deterministic(true))
	if err != nil {
		return false, fmt.Errorf("widgetimport schedule: marshal: %w", err)
	}
	return im.q.Schedule(ctx, queue.Params{Queue: Queue, Kind: Kind, Key: name, Payload: payload})
}

// Handle processes one import task: decode the batch and bulk-insert it. A
// decode failure is permanent (the payload will never parse); a store failure
// is returned as-is so the queue retries it with backoff.
func (im *Importer) Handle(ctx context.Context, t queue.Task) error {
	var ws []widget.Widget
	if err := json.Unmarshal(t.Payload, &ws); err != nil {
		return queue.Permanent(fmt.Errorf("%w: %w", ErrBadPayload, err))
	}
	if err := im.store.BulkInsert(ctx, ws); err != nil {
		return fmt.Errorf("widgetimport handle: %w", err)
	}
	return nil
}

// Config tunes the workers of the queue.
type Config struct {
	// Interval is the wait after a poll that found no task (default 1s).
	Interval time.Duration
	// BatchSize is the max tasks claimed at once (default 50).
	BatchSize int
	// Metrics is the registry for the skit_queue_* metrics of the queue and
	// the skit_worker_* metrics of its loops; nil — metrics off.
	Metrics prometheus.Registerer
}

// Workers builds the workers of Queue (queue.Workers). Run them on every
// replica for higher throughput — the queue's SKIP LOCKED claim keeps them
// from colliding. A queue.Mux routes the tasks of Kind to Handle; a task of
// another kind is dead-lettered.
func (im *Importer) Workers(cfg Config) ([]worker.Runnable, error) {
	mux := queue.NewMux()
	if err := mux.Register(Kind, im.Handle); err != nil {
		return nil, fmt.Errorf("widgetimport workers: %w", err)
	}
	return queue.Workers(im.log, im.q, queue.Config{
		Queue: Queue,
		Batch: cmp.Or(cfg.BatchSize, 50),
		Poll:  cfg.Interval,
	}, mux.Handle, queue.WithMetrics(cfg.Metrics))
}
