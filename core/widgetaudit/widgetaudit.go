// Package widgetaudit is an example broker consumer. It records every
// widget.created event it receives into the widget_event_log table, keyed by
// the CloudEvents id so at-least-once redelivery de-duplicates (ON CONFLICT DO
// NOTHING). It demonstrates the consumer side of the publish->consume flow,
// including continuing the producer's distributed trace.
package widgetaudit

import (
	"cmp"
	"context"
	"log/slog"
	"uuid"

	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/dbx"
	skotel "github.com/assanoff/skit/otel"
)

// scope names the tracer of this package.
const scope = "github.com/assanoff/skit-x/core/widgetaudit"

// Config configures a Recorder.
type Config struct {
	// TracerProvider records the span of each event; nil → the global provider.
	TracerProvider trace.TracerProvider
}

// Recorder persists received events to the audit log.
type Recorder struct {
	log    *slog.Logger
	db     *sqlx.DB
	tracer trace.Tracer
}

// New builds a Recorder.
func New(log *slog.Logger, db *sqlx.DB, cfg Config) *Recorder {
	tp := cmp.Or(cfg.TracerProvider, otel.GetTracerProvider())
	return &Recorder{log: log, db: db, tracer: tp.Tracer(scope)}
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
	ctx = skotel.ExtractFromCarrier(ctx, m.Headers) // restore the producer's span context
	ctx, span := r.tracer.Start(ctx, "widgetaudit.record",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("event.type", m.Type),
			attribute.String("event.id", m.ID),
		),
	)
	defer span.End()

	id, err := uuid.Parse(m.ID)
	if err != nil {
		skotel.SetError(span, err)
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
		skotel.SetError(span, err)
		r.log.ErrorContext(ctx, "widgetaudit: record failed, requeueing", "event_id", id, "err", err)
		return broker.Requeue
	}
	r.log.InfoContext(ctx, "widgetaudit: recorded event", "event_id", id, "type", m.Type)
	return broker.Ack
}
