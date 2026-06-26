// Package widgetaudit is an example broker consumer. It records every
// widget.created event it receives into the widget_event_log table, keyed by
// the CloudEvents id so at-least-once redelivery de-duplicates (ON CONFLICT DO
// NOTHING). It demonstrates the consumer side of the publish->consume flow,
// including continuing the producer's distributed trace.
package widgetaudit

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/otel"
)

// Recorder persists received events to the audit log.
type Recorder struct {
	log    *logger.Logger
	db     *sqlx.DB
	tracer trace.Tracer
}

// New builds a Recorder. tracer is used to continue the producer's trace from
// the event's headers; pass the application tracer (a no-op tracer is fine when
// tracing is disabled).
func New(log *logger.Logger, db *sqlx.DB, tracer trace.Tracer) *Recorder {
	return &Recorder{log: log, db: db, tracer: tracer}
}

// Handle implements broker.Handler: it writes one audit row per event. A
// malformed id is dropped (Discard) — retrying cannot fix it; a transient DB
// error requeues so the message is retried.
//
// It continues the producer's distributed trace: the outbox captured the W3C
// trace context into the event headers, the relay carried them onto the broker
// message, and here we restore that context and open a child span. So producing
// the widget and recording the audit row appear as ONE end-to-end trace, and the
// consumer's logs carry the same trace_id as the producer's.
func (r *Recorder) Handle(ctx context.Context, m broker.Message) broker.Action {
	ctx = otel.ExtractFromCarrier(ctx, m.Headers) // restore the producer's span context
	ctx = otel.InjectTracing(ctx, r.tracer)       // make the tracer + trace_id available
	ctx, span := otel.AddSpan(
		ctx, "widgetaudit.record",
		attribute.String("messaging.system", "rabbitmq"),
		attribute.String("event.type", m.Type),
		attribute.String("event.id", m.ID),
	)
	defer span.End()

	id, err := uuid.Parse(m.ID)
	if err != nil {
		span.RecordError(err)
		r.log.Warn(ctx, "widgetaudit: bad event id, dropping", "id", m.ID, "err", err)
		return broker.Discard
	}

	// received_at defaults to now() in the schema.
	const q = `
		INSERT INTO widget_event_log (event_id, type, payload)
		VALUES (:event_id, :type, :payload)
		ON CONFLICT (event_id) DO NOTHING`
	arg := struct {
		EventID uuid.UUID `db:"event_id"`
		Type    string    `db:"type"`
		Payload []byte    `db:"payload"`
	}{EventID: id, Type: m.Type, Payload: m.Data}

	if err := dbx.NamedExecContext(ctx, r.log, r.db, q, arg); err != nil {
		span.RecordError(err)
		r.log.Error(ctx, "widgetaudit: record failed, requeueing", "event_id", id, "err", err)
		return broker.Requeue
	}
	r.log.Info(ctx, "widgetaudit: recorded event", "event_id", id, "type", m.Type)
	return broker.Ack
}
