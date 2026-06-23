package deps

import (
	"github.com/assanoff/servicekit/dim"

	"github.com/assanoff/service-kit-x/internal/app/provider"
)

// initTracer initializes the application tracer (no-op when tracing is disabled).
var initTracer = func(c *Deps) (cleanup dim.CleanupFunc, err error) {
	c.Tracer, cleanup = dim.NewResource("Tracer", provider.Tracer(&c.Opts))
	return cleanup, nil
}
