// Package deps wires the application's dependencies, mirroring the
// lavka-promoaction app/deps layout: a Deps container of lazy dim.Provider
// fields and an ordered Initializers slice. The generic app.InitDeps runs each
// initializer and registers its cleanup with the global closer (LIFO shutdown).
package deps

import (
	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel/trace"

	"github.com/assanoff/servicekit/app"
	"github.com/assanoff/servicekit/auditlog"
	"github.com/assanoff/servicekit/auth"
	"github.com/assanoff/servicekit/broker"
	"github.com/assanoff/servicekit/broker/rabbitmq"
	"github.com/assanoff/servicekit/dim"
	"github.com/assanoff/servicekit/eventbus"
	"github.com/assanoff/servicekit/i18n"
	"github.com/assanoff/servicekit/logger"
	"github.com/assanoff/servicekit/outbox"
	"github.com/assanoff/servicekit/poller"
	"github.com/assanoff/servicekit/queue"
	"github.com/assanoff/servicekit/translation"

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

	Tracer      dim.Provider[trace.Tracer]
	DB          dim.Provider[*sqlx.DB]
	Queue       dim.Provider[*queue.PG]
	BrokerConn  dim.Provider[*rabbitmq.Conn]
	Publisher   dim.Provider[broker.Publisher]
	Outbox      dim.Provider[outbox.Store]
	Translator  dim.Provider[*i18n.Translator]
	Translation dim.Provider[*translation.Translator]
	Verifier    dim.Provider[auth.Verifier]
	Bus         dim.Provider[*eventbus.Bus]
	AuditLog    dim.Provider[*auditlog.Core]

	WidgetCore    dim.Provider[*widget.Core]
	WidgetImport  dim.Provider[*widgetimport.Importer]
	WidgetCount   dim.Provider[*poller.Poller[int]]
	WidgetHandler dim.Provider[*widgetapi.Handler]
	WidgetGRPC    dim.Provider[*widgetgrpc.Handler]
}

// Initializers runs in order: infrastructure first, then core, then handlers.
// It is plain data — a command needing only some dependencies passes a subset
// (e.g. slices.Concat of named groups) to app.InitDeps.
var Initializers = []app.Initializer[Deps]{
	// Core infrastructure
	initTracer,
	initStore,
	initQueue,
	initBroker,
	initTranslator,
	initTranslation,
	initAuth,
	initBus,
	initAuditLog,

	// Core business logic
	initWidgetCore,
	initWidgetImport,
	initWidgetCount,

	// Handlers
	initWidgetHandler,
	initWidgetGRPC,
}
