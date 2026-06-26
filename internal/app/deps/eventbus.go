package deps

import (
	"context"
	"net/http"

	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/eventbus"
	"github.com/assanoff/skit/httpmw"
	"github.com/assanoff/skit/worker"

	"github.com/assanoff/skit-x/core/widgetwebhook"
)

// initBus builds the in-process event bus and registers its consumers. When the
// webhook is enabled, a widgetwebhook.Notifier subscribes to (widget, created)
// and delivers each event over a resilient HTTP client (httpmw retry transport:
// retries 429/503 with backoff, honors Retry-After). The bus is always created —
// with no consumers it is a cheap no-op — so widget.Core can dispatch
// unconditionally.
var initBus = func(c *Deps) (dim.CleanupFunc, error) {
	bus := eventbus.New(c.Logger)

	if w := c.Opts.Webhook; w.Enabled {
		client := &http.Client{
			Timeout: w.Timeout,
			Transport: httpmw.NewRetryTransport(nil, httpmw.RetryConfig{
				Backoff: worker.Backoff{
					Base:        w.BackoffBase,
					Max:         w.BackoffMax,
					MaxAttempts: w.MaxAttempts,
					Jitter:      0.2,
				},
			}),
		}
		widgetwebhook.New(c.Logger, client, w.URL).Register(bus)
	}

	c.Bus = dim.OnceWithName("Bus", func(context.Context) (*eventbus.Bus, error) {
		return bus, nil
	})
	return nil, nil
}
