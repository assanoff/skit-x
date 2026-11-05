// Package widgetaudit is an example broker consumer. It records every
// widget.created event it receives into the widget_event_log table, keyed by
// the CloudEvents id and time so at-least-once redelivery de-duplicates (ON
// CONFLICT DO NOTHING). It demonstrates the consumer side of the
// publish->consume flow.
// The ON CONFLICT is what makes it exactly-once: the effect and its mark commit
// together in one database. The wiring also wraps Handle in dedup
// (internal/app/idempotency) — only to show that wrapper; a consumer whose
// effect leaves the database relies on it.
// The trace of the producer continues through the RabbitMQ consumer of the
// wiring: Handle runs under its consumer span, and its logs carry the trace_id
// of the request that created the widget.
//
// widget_event_log is partitioned by the month of the time of the event;
// Partitions keeps its partitions (the partition package of skit). An event of
// a month without a partition is requeued until its partition is made.
package widgetaudit

import (
	"context"
	"log/slog"
	"time"
	"uuid"

	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/partition"
)

// Recorder persists received events to the audit log.
type Recorder struct {
	log *slog.Logger
	db  *dbx.DB
}

// New builds a Recorder.
func New(log *slog.Logger, db *dbx.DB) *Recorder {
	return &Recorder{log: log, db: db}
}

// Partitions builds the keeper of the monthly partitions of widget_event_log:
// those of the current and the next month, and none older than retention (0
// keeps them all). Its Maintain is the tick of a worker.Loop.
func Partitions(log *slog.Logger, db *dbx.DB, retention time.Duration) (*partition.Keeper, error) {
	return partition.New(log, db, partition.Config{
		Tables:    []string{"widget_event_log"},
		Period:    partition.Month,
		Retention: retention,
	})
}

// Handle implements broker.Handler: it writes one audit row per event. A
// malformed id or an event without a time is dropped (Discard) — retrying
// cannot fix it; a transient DB error, a month without a partition included,
// requeues so the message is retried. The consumer span records either
// verdict as its error.
func (r *Recorder) Handle(ctx context.Context, m broker.Message) broker.Action {
	id, err := uuid.Parse(m.ID)
	if err != nil {
		r.log.WarnContext(ctx, "widgetaudit: bad event id, dropping", "id", m.ID, "err", err)
		return broker.Discard
	}
	if m.Time.IsZero() {
		r.log.WarnContext(ctx, "widgetaudit: event without a time, dropping", "event_id", id)
		return broker.Discard
	}

	// received_at defaults to now() in the schema.
	const q = `
		INSERT INTO widget_event_log (event_id, occurred_at, type, payload)
		VALUES (:event_id, :occurred_at, :type, :payload)
		ON CONFLICT (event_id, occurred_at) DO NOTHING`
	arg := struct {
		EventID    dbx.UUID  `db:"event_id"`
		OccurredAt time.Time `db:"occurred_at"`
		Type       string    `db:"type"`
		Payload    []byte    `db:"payload"`
	}{EventID: dbx.UUID(id), OccurredAt: m.Time, Type: m.Type, Payload: m.Data}

	if err := dbx.NamedExec(ctx, r.db, q, arg); err != nil {
		r.log.ErrorContext(ctx, "widgetaudit: record failed, requeueing", "event_id", id, "err", err)
		return broker.Requeue
	}
	r.log.InfoContext(ctx, "widgetaudit: recorded event", "event_id", id, "type", m.Type)
	return broker.Ack
}
