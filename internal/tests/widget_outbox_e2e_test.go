package tests

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/broker/rabbitmq"
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
	log := slog.New(slog.DiscardHandler)

	// Tracing: an in-memory span recorder so we can assert the consumer
	// continues the producer's trace through the outbox/broker.
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("e2e")
	db := openTracedDB(t, cfg, tp)

	amqpURL := startRabbitMQ(ctx, t)
	conn, err := rabbitmq.Dial(rabbitmq.Config{URL: amqpURL})
	if err != nil {
		t.Fatalf("dial rabbitmq: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	pub, err := rabbitmq.NewPublisher(conn, rabbitmq.PublisherConfig{Source: "skit-test", TracerProvider: tp})
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	store := outbox.NewPG(log, db, outbox.Options{})
	reg := outbox.NewRegistry()
	if err := reg.Register[widget.Created](widget.EventWidgetCreated, exchange, outbox.WithKey(routingKey)); err != nil {
		t.Fatalf("register route: %v", err)
	}
	wstore := widgetdb.NewStore(log, db)
	core := widget.NewCore(log, wstore, widget.WithOutbox(db, store, reg))

	recorder := widgetaudit.New(log, db)
	handler := broker.Instrument(recorder.Handle, broker.InstrumentConfig{
		Consumer:       "widget-audit-test",
		System:         "rabbitmq",
		TracerProvider: tp,
	})
	consumer, err := rabbitmq.NewConsumer(conn, log, rabbitmq.ConsumerConfig{
		Queue:       queue,
		Exchange:    exchange,
		RoutingKeys: []string{routingKey},
		Name:        "widget-audit-test",
	}, handler)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}

	relay := outbox.NewRelay(log, store, pub, outbox.RelayConfig{
		PollInterval:   200 * time.Millisecond,
		PublishTimeout: 3 * time.Second,
	})

	// Supervise the consumer and relay together.
	runCtx, cancel := context.WithCancel(ctx)
	group := worker.NewGroup(log, worker.GroupConfig{ShutdownTimeout: 3 * time.Second})
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

	// Wait for the consumer to record the event in the audit log.
	var (
		gotType    string
		gotPayload []byte
	)
	recorded := eventually(20*time.Second, 200*time.Millisecond, func() bool {
		return db.QueryRowContext(ctx,
			`SELECT type, payload FROM widget_event_log ORDER BY received_at DESC LIMIT 1`).
			Scan(&gotType, &gotPayload) == nil
	})
	if !recorded {
		t.Fatal("timed out waiting for the consumer to record widget.created")
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
	sent := eventually(10*time.Second, 200*time.Millisecond, func() bool {
		if err := db.GetContext(ctx, &status,
			`SELECT status FROM outbox_events WHERE type = $1 ORDER BY created_at DESC LIMIT 1`,
			widget.EventWidgetCreated); err != nil {
			t.Fatalf("query outbox status: %v", err)
		}
		return status == outbox.StatusSent
	})
	if !sent {
		t.Errorf("outbox status = %q, want sent", status)
	}

	assertQueriesTraced(t, spans, prodSpan.SpanContext())
	assertTraceContinued(t, spans, exchange, prodSpan.SpanContext())
}

// assertQueriesTraced checks that the transaction of the producer — the widget
// row and its outbox event — is recorded as query spans under the producer
// span, BEGIN and COMMIT included.
func assertQueriesTraced(t *testing.T, spans *tracetest.SpanRecorder, prod trace.SpanContext) {
	t.Helper()
	var texts []string
	for _, s := range spans.Ended() {
		if s.Parent().SpanID() != prod.SpanID() {
			continue
		}
		for _, kv := range s.Attributes() {
			if kv.Key == "db.query.text" {
				texts = append(texts, kv.Value.AsString())
			}
		}
	}
	want := []string{"begin", "INSERT INTO widgets", "INSERT INTO outbox_events", "commit"}
	if len(texts) != len(want) {
		t.Fatalf("queries under the producer span = %q, want %q", texts, want)
	}
	for i, w := range want {
		if !strings.Contains(texts[i], w) {
			t.Errorf("query %d = %q, want it to contain %q", i, texts[i], w)
		}
	}
}

// assertTraceContinued checks that the relay sent the event under a CLIENT
// span and the consumer handled it under a CONSUMER span, both children of the
// producer span (same trace id, parent = producer), so the trace flows
// producer -> outbox -> broker -> consumer; the consumer span also links the
// producer. The spans end in defers that may land just after the audit row
// commits, so it polls for them.
func assertTraceContinued(t *testing.T, spans *tracetest.SpanRecorder, exchange string, prod trace.SpanContext) {
	t.Helper()
	for _, want := range []struct {
		name string
		kind trace.SpanKind
	}{
		{"send " + exchange, trace.SpanKindClient},
		{"process " + exchange, trace.SpanKindConsumer},
	} {
		var span sdktrace.ReadOnlySpan
		found := eventually(10*time.Second, 100*time.Millisecond, func() bool {
			span = endedSpan(spans, want.name)
			return span != nil
		})
		if !found {
			t.Fatalf("timed out waiting for the span %q", want.name)
		}
		if span.SpanKind() != want.kind {
			t.Errorf("%s: kind = %s, want %s", want.name, span.SpanKind(), want.kind)
		}
		if got := span.SpanContext().TraceID(); got != prod.TraceID() {
			t.Errorf("%s: trace id = %s, want %s (trace not continued)", want.name, got, prod.TraceID())
		}
		if got := span.Parent().SpanID(); got != prod.SpanID() {
			t.Errorf("%s: parent = %s, want producer span %s", want.name, got, prod.SpanID())
		}
	}
	process := endedSpan(spans, "process "+exchange)
	if links := process.Links(); len(links) != 1 || links[0].SpanContext.SpanID() != prod.SpanID() {
		t.Errorf("consumer span links = %v, want one link to the producer span", links)
	}
}

// eventually polls cond every interval until it holds or timeout elapses, and
// reports whether it held.
func eventually(timeout, interval time.Duration, cond func() bool) bool {
	deadline := time.After(timeout)
	for !cond() {
		select {
		case <-deadline:
			return false
		case <-time.After(interval):
		}
	}
	return true
}

// endedSpan returns the first ended span recorded under name, or nil.
func endedSpan(spans *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	for _, s := range spans.Ended() {
		if s.Name() == name {
			return s
		}
	}
	return nil
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
