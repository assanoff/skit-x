package provider

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/assanoff/servicekit/dim"
	skotel "github.com/assanoff/servicekit/otel"

	"github.com/assanoff/service-kit-x/internal/app/config"
)

// Tracer returns a factory for the application tracer. When tracing is disabled
// it yields a no-op tracer with no cleanup; otherwise it bootstraps OTLP export
// and returns a shutdown that flushes pending spans.
func Tracer(opts *config.ServerOpts) func(ctx context.Context) (trace.Tracer, dim.CleanupFunc, error) {
	return func(ctx context.Context) (trace.Tracer, dim.CleanupFunc, error) {
		if !opts.OTEL.Enabled {
			return noop.NewTracerProvider().Tracer(opts.Service), nil, nil
		}

		tracer, shutdown, err := skotel.InitTracing(ctx, skotel.Config{
			ServiceName: opts.Service,
			Endpoint:    opts.OTEL.Endpoint,
			Insecure:    opts.OTEL.Insecure,
			Probability: opts.OTEL.Probability,
			ExcludedRoutes: map[string]struct{}{
				"GET /healthz": {},
				"GET /readyz":  {},
			},
		})
		if err != nil {
			return nil, nil, err
		}

		cleanup := func() error { return shutdown(context.Background()) }
		return tracer, cleanup, nil
	}
}
