// Package deps wires the application's dependencies, mirroring the
// lavka-promoaction app/deps layout: a Deps container of lazy dim.Provider
// fields, an ordered Initializers slice, and InitDeps which runs each
// initializer and registers its cleanup with the global closer (LIFO shutdown).
package deps

import (
	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel/trace"

	"github.com/assanoff/servicekit/auth"
	"github.com/assanoff/servicekit/broker"
	"github.com/assanoff/servicekit/broker/rabbitmq"
	"github.com/assanoff/servicekit/closer"
	"github.com/assanoff/servicekit/dim"
	"github.com/assanoff/servicekit/i18n"
	"github.com/assanoff/servicekit/logger"
	"github.com/assanoff/servicekit/outbox"
	"github.com/assanoff/servicekit/queue"

	widgetapi "github.com/assanoff/service-kit-x/api/widget"
	"github.com/assanoff/service-kit-x/core/widget"
	"github.com/assanoff/service-kit-x/core/widgetimport"
	"github.com/assanoff/service-kit-x/internal/app/config"
	"github.com/assanoff/service-kit-x/internal/app/handlers/widgetgrpc"
)

// Deps holds application dependencies as lazy providers.
type Deps struct {
	Opts   config.ServerOpts
	Logger *logger.Logger

	Tracer     dim.Provider[trace.Tracer]
	DB         dim.Provider[*sqlx.DB]
	Queue      dim.Provider[*queue.PG]
	BrokerConn dim.Provider[*rabbitmq.Conn]
	Publisher  dim.Provider[broker.Publisher]
	Outbox     dim.Provider[outbox.Store]
	Translator dim.Provider[*i18n.Translator]
	Verifier   dim.Provider[auth.Verifier]

	WidgetCore    dim.Provider[*widget.Core]
	WidgetImport  dim.Provider[*widgetimport.Importer]
	WidgetHandler dim.Provider[*widgetapi.Handler]
	WidgetGRPC    dim.Provider[*widgetgrpc.Handler]
}

// Initializers runs in order: infrastructure first, then core, then handlers.
var Initializers = []func(*Deps) (dim.CleanupFunc, error){
	// Core infrastructure
	initTracer,
	initStore,
	initQueue,
	initBroker,
	initTranslator,
	initAuth,

	// Core business logic
	initWidgetCore,
	initWidgetImport,

	// Handlers
	initWidgetHandler,
	initWidgetGRPC,
}

// InitDeps runs all initializers and registers their cleanups with the global
// closer, which executes them LIFO on shutdown.
func InitDeps(c *Deps, initializers []func(*Deps) (dim.CleanupFunc, error)) error {
	for _, fn := range initializers {
		cleanup, err := fn(c)
		if err != nil {
			return err
		}
		if cleanup != nil {
			closer.Add(cleanup)
		}
	}
	return nil
}
