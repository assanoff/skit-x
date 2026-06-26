// Package deps wires the application's dependencies, mirroring the
// lavka-promoaction app/deps layout: a Deps container of lazy dim.Provider
// fields and an ordered Initializers slice. The generic app.InitDeps runs each
// initializer and registers its cleanup with the global closer (LIFO shutdown).
package deps

import (
	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel/trace"

	"github.com/assanoff/skit/app"
	"github.com/assanoff/skit/auditlog"
	"github.com/assanoff/skit/auth"
	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/broker/rabbitmq"
	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/eventbus"
	"github.com/assanoff/skit/i18n"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/poller"
	"github.com/assanoff/skit/queue"
	"github.com/assanoff/skit/translation"

	productapi "github.com/assanoff/skit-x/api/product"
	userapi "github.com/assanoff/skit-x/api/user"
	widgetapi "github.com/assanoff/skit-x/api/widget"
	"github.com/assanoff/skit-x/core/product"
	"github.com/assanoff/skit-x/core/user"
	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widgetimport"
	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/handlers/widgetgrpc"
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

	UserCore    dim.Provider[*user.Core]
	UserHandler dim.Provider[*userapi.Handler]

	ProductCore    dim.Provider[*product.Core]
	ProductHandler dim.Provider[*productapi.Handler]
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
	initUserCore,
	initProductCore,

	// Handlers
	initWidgetHandler,
	initWidgetGRPC,
	initUserHandler,
	initProductHandler,
}

// Storage is the minimal initializer set for DB-only commands (e.g. a one-shot
// CLI job): it wires just the Postgres store. Compose subsets like this and pass
// them to app.RunCommand so a command connects only what it needs.
var Storage = []app.Initializer[Deps]{initStore}
