package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assanoff/skit/eventbus"
	"github.com/assanoff/skit/httpmw"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/worker"

	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widget/widgetdb"
	"github.com/assanoff/skit-x/core/widgetwebhook"
)

// TestWidgetWebhookHTTPMW exercises the in-process eventbus + resilient HTTP
// client demo together: creating a widget dispatches a (widget, created) event
// on the bus; the widgetwebhook consumer POSTs it to a flaky receiver that fails
// once with 503, and the httpmw retry transport recovers on the next attempt.
// Dispatch is synchronous, so the delivery (and its retry) completes within
// Create.
func TestWidgetWebhookHTTPMW(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	cfg := startPostgres(ctx, t)
	db := openTestDB(t, cfg)
	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})

	// Flaky webhook: first call returns 503, subsequent calls 200. Records the
	// payload from the successful call.
	var (
		attempts atomic.Int32
		mu       sync.Mutex
		gotBody  []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: httpmw.NewRetryTransport(nil, httpmw.RetryConfig{
			Backoff: worker.Backoff{
				Base: 10 * time.Millisecond, Max: 100 * time.Millisecond,
				MaxAttempts: 4, Jitter: 0.1,
			},
		}),
	}

	bus := eventbus.New(log)
	widgetwebhook.New(log, client, srv.URL).Register(bus)

	store := widgetdb.NewStore(log, db)
	core := widget.NewCore(log, store, widget.WithEventBus(bus))

	w, err := core.Create(ctx, widget.NewWidget{Name: "hooked", Description: "via eventbus"})
	if err != nil {
		t.Fatalf("create widget: %v", err)
	}

	// The webhook should have been called at least twice (one 503 + one success),
	// proving the retry transport recovered the throttled response.
	if got := attempts.Load(); got < 2 {
		t.Fatalf("expected the retry transport to call the webhook >=2 times, got %d", got)
	}

	mu.Lock()
	body := gotBody
	mu.Unlock()
	var payload struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode webhook payload: %v", err)
	}
	if payload.ID != w.ID.String() || payload.Name != "hooked" {
		t.Errorf("webhook payload mismatch: got %+v, want id=%s name=hooked", payload, w.ID)
	}
}
