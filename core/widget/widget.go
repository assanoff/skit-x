// Package widget is an example business module. It demonstrates the skit
// conventions: the Core holds business logic and depends only on a Store
// interface declared here, while the concrete SQL implementation lives in a
// nested package (widgetdb). This keeps the domain testable and storage-agnostic.
package widget

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/assanoff/skit/auditlog/auditbus"
	"github.com/assanoff/skit/auth"
	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/errs"
	"github.com/assanoff/skit/eventbus"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/order"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/page"
)

// Store is the persistence contract for widgets. The Core depends on this
// interface; concrete implementations (e.g. widgetdb) live elsewhere. WithTx
// yields a sibling bound to a transaction so a write can commit atomically with
// an outbox event (see Create).
type Store interface {
	WithTx(tx sqlx.ExtContext) Store
	Create(ctx context.Context, w Widget) error
	Update(ctx context.Context, w Widget) error
	Delete(ctx context.Context, id uuid.UUID) error
	QueryByID(ctx context.Context, id uuid.UUID) (Widget, error)
	Query(ctx context.Context, filter QueryFilter, by order.By, pg page.Page) ([]Widget, error)
	QueryByCursor(ctx context.Context, filter QueryFilter, cur page.Cursor) (items []Widget, next string, err error)
	Count(ctx context.Context, filter QueryFilter) (int, error)
}

// Core implements the widget business logic.
type Core struct {
	log   *logger.Logger
	store Store

	// Optional transactional-outbox wiring. When set, Create persists the widget
	// and a widget.created event in one transaction. When nil, Create just writes
	// the widget (no event) — so the example runs without a broker too.
	db     *sqlx.DB
	outbox outbox.Store
	reg    *outbox.Registry

	// Optional in-process event bus. When set, Create dispatches a synchronous,
	// best-effort widget.created event after the write (the in-process complement
	// to the durable outbox above). nil disables in-process dispatch.
	bus *eventbus.Bus
}

// Option customizes a Core.
type Option func(*Core)

// WithOutbox enables transactional event publishing: Create writes the widget
// and a widget.created event atomically via outbox.WithinTran. The event's
// transport route (topic/key) is resolved from reg, registered at startup — the
// domain code names neither an exchange nor a routing key.
func WithOutbox(db *sqlx.DB, store outbox.Store, reg *outbox.Registry) Option {
	return func(c *Core) {
		c.db = db
		c.outbox = store
		c.reg = reg
	}
}

// WithEventBus enables in-process event dispatch: after a widget is created,
// Create publishes a (widget, created) event on the bus for any registered
// in-process consumers (e.g. a webhook notifier). Dispatch is synchronous and
// best-effort — a consumer's failure is logged by the bus but does not roll back
// the write or fail the request. This is independent of WithOutbox: a Core may
// use neither, either, or both.
func WithEventBus(bus *eventbus.Bus) Option {
	return func(c *Core) { c.bus = bus }
}

// NewCore constructs a Core.
func NewCore(log *logger.Logger, store Store, opts ...Option) *Core {
	c := &Core{log: log, store: store}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Create validates and persists a new widget. With outbox wiring it also emits
// a widget.created event in the same transaction (transactional outbox); without
// it, the widget is written directly.
func (c *Core) Create(ctx context.Context, nw NewWidget) (Widget, error) {
	now := time.Now().UTC()
	w := Widget{
		ID:          uuid.New(),
		Name:        nw.Name,
		Description: nw.Description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	var err error
	if c.outbox == nil {
		err = c.store.Create(ctx, w)
	} else {
		// The domain emits a plain typed event; it knows nothing about the
		// transaction's mechanics or the transport. pub records it in the same
		// transaction as the widget write, so both commit (or roll back) together.
		err = outbox.WithinTran(ctx, c.log, c.db, c.outbox, c.reg, func(tx *sqlx.Tx, pub outbox.Publisher) error {
			if cerr := c.store.WithTx(tx).Create(ctx, w); cerr != nil {
				return cerr
			}
			return pub.Publish(ctx, Created{
				ID:          w.ID.String(),
				Name:        w.Name,
				Description: w.Description,
				CreatedAt:   w.CreatedAt,
			})
		})
	}
	if err != nil {
		if errors.Is(err, dbx.ErrDBDuplicatedEntry) {
			return Widget{}, errs.Newf(errs.AlreadyExists, "widget %q already exists", nw.Name).
				WithMessageID("widget.already_exists").
				WithArgs(map[string]any{"name": nw.Name})
		}
		return Widget{}, errs.New(errs.Internal, err)
	}

	// In-process, synchronous, best-effort notification. Unlike the outbox above
	// (durable, transactional, cross-service), these handlers run now on this
	// goroutine; their failure is logged by the bus but never fails the create.
	if c.bus != nil {
		_ = c.bus.Publish(ctx, eventbus.MustData(EventBusDomain, EventBusActionCreated, Created{
			ID:          w.ID.String(),
			Name:        w.Name,
			Description: w.Description,
			CreatedAt:   w.CreatedAt,
		}))
	}
	c.publishAudit(ctx, w)
	return w, nil
}

// Count returns the number of widgets matching filter. With the zero filter it is
// the total, which backs the cached widget-count poller (see the app wiring) —
// the poller passes an empty QueryFilter so hot read paths serve the count
// without hitting the database each call.
func (c *Core) Count(ctx context.Context, filter QueryFilter) (int, error) {
	n, err := c.store.Count(ctx, filter)
	if err != nil {
		return 0, errs.New(errs.Internal, err)
	}
	return n, nil
}

// QueryByID returns a widget or a NotFound error.
func (c *Core) QueryByID(ctx context.Context, id uuid.UUID) (Widget, error) {
	w, err := c.store.QueryByID(ctx, id)
	if err != nil {
		if errors.Is(err, dbx.ErrDBNotFound) {
			return Widget{}, errs.Newf(errs.NotFound, "widget %s not found", id).
				WithMessageID("widget.not_found").
				WithArgs(map[string]any{"id": id.String()})
		}
		return Widget{}, errs.New(errs.Internal, err)
	}
	return w, nil
}

// Query returns one page of widgets matching filter, ordered by.
func (c *Core) Query(ctx context.Context, filter QueryFilter, by order.By, pg page.Page) ([]Widget, error) {
	ws, err := c.store.Query(ctx, filter, by, pg)
	if err != nil {
		return nil, errs.New(errs.Internal, err)
	}
	return ws, nil
}

// QueryByCursor returns up to cur.Limit() widgets matching filter, newest-first,
// starting after the keyset boundary the cursor encodes (the first page when its
// token is empty), plus an opaque next-page cursor token — empty when there are
// no more rows. Unlike Query (offset), it is stable under concurrent inserts and
// stays cheap at any depth. Prev paging is not offered here (forward-only feed).
func (c *Core) QueryByCursor(ctx context.Context, filter QueryFilter, cur page.Cursor) ([]Widget, string, error) {
	ws, next, err := c.store.QueryByCursor(ctx, filter, cur)
	if err != nil {
		return nil, "", errs.New(errs.Internal, err)
	}
	return ws, next, nil
}

// Update applies a partial update and persists it.
func (c *Core) Update(ctx context.Context, id uuid.UUID, uw UpdateWidget) (Widget, error) {
	w, err := c.QueryByID(ctx, id)
	if err != nil {
		return Widget{}, err
	}

	if uw.Name != nil {
		w.Name = *uw.Name
	}
	if uw.Description != nil {
		w.Description = *uw.Description
	}
	w.UpdatedAt = time.Now().UTC()

	if err := c.store.Update(ctx, w); err != nil {
		return Widget{}, errs.New(errs.Internal, err)
	}
	c.publishAudit(ctx, w)
	return w, nil
}

// publishAudit emits a best-effort audit event for w on the in-process bus. The
// auditbus consumer (wired at startup) records a versioned snapshot. Doing this in
// the domain — not in a transport middleware — means every path (REST, gRPC,
// workers) that mutates a widget is audited the same way.
func (c *Core) publishAudit(ctx context.Context, w Widget) {
	if c.bus == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"id":          w.ID.String(),
		"name":        w.Name,
		"description": w.Description,
		"created_at":  w.CreatedAt,
		"updated_at":  w.UpdatedAt,
	})
	if err != nil {
		return
	}
	_ = auditbus.Publish(ctx, c.bus, auditbus.Event{
		ModelType: AuditModelType,
		ModelID:   w.ID.String(),
		CreatedBy: auditActor(ctx),
		Payload:   payload,
	})
}

// auditActor resolves the acting principal's subject for audit attribution.
func auditActor(ctx context.Context) string {
	if p, ok := auth.PrincipalFromContext(ctx); ok {
		return p.Subject
	}
	return ""
}

// Delete removes a widget.
func (c *Core) Delete(ctx context.Context, id uuid.UUID) error {
	if err := c.store.Delete(ctx, id); err != nil {
		if errors.Is(err, dbx.ErrDBNotFound) {
			return errs.Newf(errs.NotFound, "widget %s not found", id).
				WithMessageID("widget.not_found").
				WithArgs(map[string]any{"id": id.String()})
		}
		return errs.New(errs.Internal, err)
	}
	return nil
}
