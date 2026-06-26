package tests

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/assanoff/skit/broker/rabbitmq"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/worker"

	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widget/widgetdb"
	"github.com/assanoff/skit-x/core/widgetaudit"
)

// TestWidgetOutboxE2E exercises the full reliable publish->consume pipeline
// against real Postgres + RabbitMQ: creating a widget atomically writes a
// widget.created outbox event; the Relay publishes it to RabbitMQ; the
// widgetaudit consumer receives it and records an audit row.
func TestWidgetOutboxE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	const (
		exchange   = "skit.widgets.test"
		routingKey = "widget.created"
		queue      = "skit.widget-audit.test"
	)

	ctx := context.Background()
	cfg := startPostgres(ctx, t)
	db := openTestDB(t, cfg)
	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})

	// Tracing: a W3C propagator plus an in-memory span recorder so we can assert
	// the consumer continues the producer's trace through the outbox/broker.
	otel.SetTextMapPropagator(propagation.TraceContext{})
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("e2e")

	amqpURL := startRabbitMQ(ctx, t)
	conn, err := rabbitmq.Dial(log, rabbitmq.Config{URL: amqpURL})
	if err != nil {
		t.Fatalf("dial rabbitmq: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	pub, err := rabbitmq.NewPublisher(conn, "skit-test", log)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	store := outbox.NewPG(log, db, outbox.Options{})
	reg := outbox.NewRegistry()
	outbox.Register[widget.Created](reg, widget.EventWidgetCreated, exchange, outbox.WithKey(routingKey))
	wstore := widgetdb.NewStore(log, db)
	core := widget.NewCore(log, wstore, widget.WithOutbox(db, store, reg))

	recorder := widgetaudit.New(log, db, tracer)
	consumer, err := rabbitmq.NewConsumer(conn, log, rabbitmq.ConsumerConfig{
		Queue:       queue,
		Exchange:    exchange,
		RoutingKeys: []string{routingKey},
		Name:        "widget-audit-test",
	}, recorder.Handle)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}

	relay := outbox.NewRelay(log, store, pub, outbox.RelayConfig{
		PollInterval:   200 * time.Millisecond,
		PublishTimeout: 3 * time.Second,
	})

	// Supervise the consumer and relay together.
	runCtx, cancel := context.WithCancel(ctx)
	group := worker.NewGroup(log.Slog(), 3*time.Second)
	group.Add(consumer)
	group.Add(relay)
	groupDone := make(chan error, 1)
	go func() { groupDone <- group.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-groupDone })

	// Let the consumer declare the exchange/queue/binding before producing.
	time.Sleep(time.Second)

	// Create a widget under a producer span — this writes the widget row and a
	// widget.created outbox event in one transaction; the event captures this
	// span's W3C trace context into its headers.
	prodCtx, prodSpan := tracer.Start(ctx, "test.create-widget")
	w, err := core.Create(prodCtx, widget.NewWidget{Name: "e2e-gadget", Description: "from outbox"})
	prodSpan.End()
	if err != nil {
		t.Fatalf("create widget: %v", err)
	}
	wantTraceID := prodSpan.SpanContext().TraceID()

	// Wait for the consumer to record the event in the audit log.
	deadline := time.After(20 * time.Second)
	var (
		gotType    string
		gotPayload []byte
	)
	for {
		err := db.QueryRowContext(ctx,
			`SELECT type, payload FROM widget_event_log ORDER BY received_at DESC LIMIT 1`).
			Scan(&gotType, &gotPayload)
		if err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the consumer to record widget.created")
		case <-time.After(200 * time.Millisecond):
		}
	}

	if gotType != widget.EventWidgetCreated {
		t.Errorf("audit type = %q, want %q", gotType, widget.EventWidgetCreated)
	}
	var payload struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(gotPayload, &payload); err != nil {
		t.Fatalf("decode audit payload: %v", err)
	}
	if payload.ID != w.ID.String() || payload.Name != "e2e-gadget" {
		t.Errorf("audit payload mismatch: got %+v, want id=%s name=e2e-gadget", payload, w.ID)
	}

	// The outbox row reaches its terminal sent state. The relay marks it sent
	// after a successful publish, which can land just after the consumer recorded
	// the audit row above, so poll rather than read once.
	var status string
	statusDeadline := time.After(10 * time.Second)
	for {
		if err := db.GetContext(ctx, &status,
			`SELECT status FROM outbox_events WHERE type = $1 ORDER BY created_at DESC LIMIT 1`,
			widget.EventWidgetCreated); err != nil {
			t.Fatalf("query outbox status: %v", err)
		}
		if status == outbox.StatusSent {
			break
		}
		select {
		case <-statusDeadline:
			t.Errorf("outbox status = %q, want sent", status)
		case <-time.After(200 * time.Millisecond):
			continue
		}
		break
	}

	// Trace continuation: the consumer should have opened a span that is a child
	// of the producer span (same trace id, parent = producer), proving the trace
	// flowed producer -> outbox -> broker -> consumer. The span ends in Handle's
	// defer, which may land just after the audit row commits, so poll for it.
	findSpan := func(name string) sdktrace.ReadOnlySpan {
		for _, s := range spans.Ended() {
			if s.Name() == name {
				return s
			}
		}
		return nil
	}
	var consumerSpan sdktrace.ReadOnlySpan
	spanDeadline := time.After(10 * time.Second)
	for consumerSpan == nil {
		if consumerSpan = findSpan("widgetaudit.record"); consumerSpan != nil {
			break
		}
		select {
		case <-spanDeadline:
			t.Fatal("timed out waiting for the consumer span")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if got := consumerSpan.SpanContext().TraceID(); got != wantTraceID {
		t.Errorf("consumer span trace id = %s, want %s (trace not continued)", got, wantTraceID)
	}
	if got := consumerSpan.Parent().SpanID(); got != prodSpan.SpanContext().SpanID() {
		t.Errorf("consumer span parent = %s, want producer span %s", got, prodSpan.SpanContext().SpanID())
	}
}

// startRabbitMQ launches a RabbitMQ container and returns its AMQP URL.
func startRabbitMQ(ctx context.Context, t *testing.T) string {
	t.Helper()
	c, err := tcrabbitmq.Run(ctx, "rabbitmq:3.13-management-alpine")
	if err != nil {
		t.Fatalf("start rabbitmq: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	amqpURL, err := c.AmqpURL(ctx)
	if err != nil {
		t.Fatalf("rabbitmq amqp url: %v", err)
	}
	return amqpURL
}
