package widget

import (
	"time"

	"github.com/google/uuid"

	"github.com/assanoff/skit/order"
)

// EventWidgetCreated is the CloudEvents type published when a widget is created.
const EventWidgetCreated = "widget.created"

// AuditModelType is the auditlog model type for widgets, shared by the REST and
// gRPC transports so a widget's audit history is keyed consistently.
const AuditModelType = "widget"

// EventBusDomain and the EventBusAction* constants name the in-process events
// dispatched on the eventbus — the synchronous, in-process complement to the
// transactional outbox. Consumers register handlers for (EventBusDomain, action)
// without importing this package's Core; the producer only names the event.
const (
	EventBusDomain        = "widget"
	EventBusActionCreated = "created"
)

// Widget is the domain entity.
type Widget struct {
	ID          uuid.UUID
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewWidget is the data required to create a Widget.
type NewWidget struct {
	Name        string
	Description string
}

// UpdateWidget is the data allowed when updating a Widget. Nil fields are left
// unchanged.
type UpdateWidget struct {
	Name        *string
	Description *string
}

// QueryFilter narrows a widget listing. Nil fields are not applied, so the zero
// value matches every widget. It is honored by both list paths (offset Query and
// cursor QueryByCursor) and by Count, so a filtered total stays consistent with
// the filtered page.
type QueryFilter struct {
	Name        *string // case-insensitive substring match on name
	Description *string // case-insensitive substring match on description
}

// Order-by field names the offset listing accepts (the cursor listing is fixed to
// the keyset order). These are the allowlist keys; the store maps them to columns.
const (
	OrderByCreatedAt = "created_at"
	OrderByName      = "name"
)

// SortableFields is the order.Parse allowlist for widget listings: each accepted
// ?order_by field maps to itself (the store turns it into a column). A field
// outside this set is rejected, so a client cannot order by an arbitrary column.
var SortableFields = map[string]string{
	OrderByCreatedAt: OrderByCreatedAt,
	OrderByName:      OrderByName,
}

// DefaultOrder is newest-first, applied when ?order_by is absent.
var DefaultOrder = order.NewBy(OrderByCreatedAt, order.DESC)

// Created is the domain event emitted when a widget is created. It is a plain
// payload type: the domain publishes it through outbox.Publisher and the
// Registry (wired at startup) maps it to its transport route. Register it once
// with outbox.Register[Created](reg, EventWidgetCreated, topic, ...).
type Created struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}
