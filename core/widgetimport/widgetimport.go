// Package widgetimport is an example business module showing reliable background
// processing with skit: callers enqueue batch-import jobs onto a durable
// queue, and a worker.Processor drains the queue, bulk-inserting each batch.
//
// It ties together three SDK pieces:
//   - queue:  durable, SKIP LOCKED work queue (safe across replicas);
//   - worker.Processor: the claim -> handle -> ack/retry loop;
//   - dbx.BulkInsert: efficient multi-row writes (via widget's store).
//
// Delivery is at-least-once, so Handle must be idempotent — the store's
// BulkInsert uses ON CONFLICT DO NOTHING to make replays harmless.
package widgetimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/queue"
	"github.com/assanoff/skit/worker"

	"github.com/assanoff/skit-x/core/widget"
)

// Kind is the queue task kind handled by this module.
const Kind = "widget-import"

// ErrBadPayload marks a permanently undecodable task payload. It is classified
// as terminal so the processor dead-letters the task instead of retrying it
// forever.
var ErrBadPayload = errors.New("widgetimport: bad payload")

// Store is the persistence contract: a batched, idempotent widget writer.
// Satisfied by widgetdb.Store.
type Store interface {
	BulkInsert(ctx context.Context, ws []widget.Widget) error
}

// Importer enqueues and processes widget-import batches.
type Importer struct {
	log   *logger.Logger
	q     queue.Queue
	store Store
}

// New builds an Importer over the given durable queue and store.
func New(log *logger.Logger, q queue.Queue, store Store) *Importer {
	return &Importer{log: log, q: q, store: store}
}

// Schedule enqueues a batch of widgets for import under the dedup key name
// (pass "" for a one-off). It returns inserted=false when name already exists.
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
	payload, err := json.Marshal(ws)
	if err != nil {
		return false, fmt.Errorf("widgetimport schedule: marshal: %w", err)
	}
	return im.q.Schedule(ctx, queue.ScheduleParams{Name: name, Kind: Kind, Payload: payload})
}

// Handle processes one import task: decode the batch and bulk-insert it. A
// decode failure is terminal (the payload will never parse); a store failure is
// returned as-is so the processor retries it.
func (im *Importer) Handle(ctx context.Context, t queue.Task) error {
	var ws []widget.Widget
	if err := json.Unmarshal(t.Payload, &ws); err != nil {
		return fmt.Errorf("%w: %w", ErrBadPayload, err)
	}
	if err := im.store.BulkInsert(ctx, ws); err != nil {
		return fmt.Errorf("widgetimport handle: %w", err)
	}
	return nil
}

// Config tunes the processing loop.
type Config struct {
	// Interval is the queue poll rate (default 1s).
	Interval time.Duration
	// BatchSize is the max tasks leased per tick (default 50).
	BatchSize int
}

// NewLoop builds the background worker.Loop that drains the queue. Run several
// (in one process via worker.Group, or across replicas) for higher throughput —
// the queue's SKIP LOCKED claim keeps them from colliding.
func (im *Importer) NewLoop(cfg Config) *worker.Loop {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}

	proc := worker.NewProcessor[queue.Task](
		im.log.Slog(),
		im.q, // Source: Claim
		worker.HandlerFunc[queue.Task](im.Handle),
		im.q, // Sink: MarkDone/MarkFailed
		worker.ProcessorConfig{
			Name:       Kind,
			BatchSize:  cfg.BatchSize,
			IsTerminal: func(err error) bool { return errors.Is(err, ErrBadPayload) },
		},
	)
	return worker.NewLoop(im.log.Slog(), worker.LoopConfig{
		Name:               Kind,
		Interval:           cfg.Interval,
		ImmediateFirstTick: true,
	}, proc.Tick())
}
