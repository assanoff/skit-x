package widget

import (
	"time"

	"github.com/google/uuid"
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
