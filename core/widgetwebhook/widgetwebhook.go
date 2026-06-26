// Package widgetwebhook delivers widget.created notifications to an external
// webhook over HTTP. It is an in-process eventbus consumer: it registers a
// handler for the (widget, created) event and POSTs the payload using a
// resilient HTTP client (an httpmw retry transport), so a flaky receiver that
// returns 429/503 is retried with backoff and honors Retry-After.
//
// It demonstrates two skit packages working together — eventbus (in-process
// fan-out) and httpmw (resilient outbound calls) — while keeping the dependency
// one-way: the producer (widget.Core) never imports this package; this package
// imports only the shared event contract (widget.Created).
package widgetwebhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/assanoff/skit/eventbus"
	"github.com/assanoff/skit/logger"

	"github.com/assanoff/skit-x/core/widget"
)

// Notifier POSTs widget.created events to a webhook URL using a (typically
// retrying) HTTP client.
type Notifier struct {
	log    *logger.Logger
	client *http.Client
	url    string
}

// New builds a Notifier. client should carry the resilient transport (e.g.
// httpmw.NewRetryTransport) and any per-request timeout.
func New(log *logger.Logger, client *http.Client, url string) *Notifier {
	return &Notifier{log: log, client: client, url: url}
}

// Register wires the notifier as a handler for the (widget, created) event. Call
// once at startup. The producer dispatches with Bus.Publish, so a delivery error
// returned here is logged by the bus but does not affect the producer.
func (n *Notifier) Register(bus *eventbus.Bus) {
	bus.Register(widget.EventBusDomain, widget.EventBusActionCreated, n.handle)
}

func (n *Notifier) handle(ctx context.Context, d eventbus.Data) error {
	ev, err := eventbus.Decode[widget.Created](d)
	if err != nil {
		return err
	}

	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("widgetwebhook: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("widgetwebhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("widgetwebhook: post %s: %w", n.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("widgetwebhook: %s returned %d", n.url, resp.StatusCode)
	}

	n.log.Info(ctx, "widget.created delivered to webhook", "id", ev.ID, "url", n.url)
	return nil
}
