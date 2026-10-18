package deps

import (
	"context"

	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/otel/otlp"
	"github.com/assanoff/skit/otel/setup"
)

// initTelemetry installs the OpenTelemetry SDK and sets c.TracerProvider. It
// runs first, so its cleanup runs last and flushes the spans of the stopping
// servers. The OTEL_* variables of the OpenTelemetry specification configure
// it: OTEL_TRACES_EXPORTER (otlp or none), OTEL_TRACES_SAMPLER and
// OTEL_TRACES_SAMPLER_ARG, OTEL_EXPORTER_OTLP_ENDPOINT. With export off, log
// lines and outgoing calls still carry a trace id. target_info and
// skit_otel_exported_spans_total go to c.Metrics, which the Deps literal
// creates before any initializer.
var initTelemetry = func(c *Deps) (dim.CleanupFunc, error) {
	ctx := context.Background()
	exp, err := otlp.NewExporter(ctx) // nil when OTEL_TRACES_EXPORTER=none
	if err != nil {
		return nil, err
	}
	tel, err := setup.New(ctx, c.Logger, setup.Config{
		Service:     c.Opts.Service,
		Version:     c.Opts.Version,
		Environment: c.Opts.Env,
		Exporter:    exp,
		Metrics:     c.Metrics,
		Global:      true,
	})
	if err != nil {
		return nil, err
	}
	c.TracerProvider = tel.TracerProvider()
	return tel.Shutdown, nil
}
