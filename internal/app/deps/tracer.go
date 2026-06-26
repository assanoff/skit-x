package deps

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/otel"
	"github.com/assanoff/skit/provider"
)

// initTracer initializes the application tracer. When tracing is disabled it
// assigns a no-op tracer (no cleanup); otherwise it uses the SDK's Tracer
// provider to bootstrap OTLP export, excluding the health probes from sampling.
var initTracer = func(c *Deps) (cleanup dim.CleanupFunc, err error) {
	if !c.Opts.OTEL.Enabled {
		svc := c.Opts.Service
		c.Tracer = func(context.Context) trace.Tracer {
			return noop.NewTracerProvider().Tracer(svc)
		}
		return nil, nil
	}

	c.Tracer, cleanup = dim.NewResource("Tracer", provider.Tracer(otel.Config{
		ServiceName: c.Opts.Service,
		Endpoint:    c.Opts.OTEL.Endpoint,
		Insecure:    c.Opts.OTEL.Insecure,
		Probability: c.Opts.OTEL.Probability,
		ExcludedRoutes: map[string]struct{}{
			"GET /healthz": {},
			"GET /readyz":  {},
		},
	}))
	return cleanup, nil
}
