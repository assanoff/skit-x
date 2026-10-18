// Package widgetaudit is an example broker consumer. It records every
// widget.created event it receives into the widget_event_log table, keyed by
// the CloudEvents id so at-least-once redelivery de-duplicates (ON CONFLICT DO
// NOTHING). It demonstrates the consumer side of the publish->consume flow.
// The trace of the producer continues through broker.Instrument, which wraps
// Handle in the wiring: Handle runs under the consumer span, and its logs carry
// the trace_id of the request that created the widget.
package widgetaudit

import (
	"context"
	"log/slog"
	"uuid"

	"github.com/jmoiron/sqlx"

	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/dbx"
)

// Recorder persists received events to the audit log.
type Recorder struct {
	log *slog.Logger
	db  *sqlx.DB
}

// New builds a Recorder.
func New(log *slog.Logger, db *sqlx.DB) *Recorder {
	return &Recorder{log: log, db: db}
}

// Handle implements broker.Handler: it writes one audit row per event. A
// malformed id is dropped (Discard) — retrying cannot fix it; a transient DB
// error requeues so the message is retried. The consumer span of
// broker.Instrument records either verdict as its error.
func (r *Recorder) Handle(ctx context.Context, m broker.Message) broker.Action {
	id, err := uuid.Parse(m.ID)
	if err != nil {
		r.log.WarnContext(ctx, "widgetaudit: bad event id, dropping", "id", m.ID, "err", err)
		return broker.Discard
	}

	// received_at defaults to now() in the schema.
	const q = `
		INSERT INTO widget_event_log (event_id, type, payload)
		VALUES (:event_id, :type, :payload)
		ON CONFLICT (event_id) DO NOTHING`
	arg := struct {
		EventID dbx.UUID `db:"event_id"`
		Type    string   `db:"type"`
		Payload []byte   `db:"payload"`
	}{EventID: dbx.UUID(id), Type: m.Type, Payload: m.Data}

	if err := dbx.NamedExecContext(ctx, r.log, r.db, q, arg); err != nil {
		r.log.ErrorContext(ctx, "widgetaudit: record failed, requeueing", "event_id", id, "err", err)
		return broker.Requeue
	}
	r.log.InfoContext(ctx, "widgetaudit: recorded event", "event_id", id, "type", m.Type)
	return broker.Ack
}
