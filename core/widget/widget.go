// Package widget is an example business module. It demonstrates the skit
// conventions: the Core holds business logic and depends only on a Store
// interface declared here, while the concrete SQL implementation lives in a
// nested package (widgetdb). This keeps the domain testable and storage-agnostic.
package widget

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
	"uuid"

	"github.com/assanoff/skit/auditlog/auditbus"
	"github.com/assanoff/skit/auth"
	"github.com/assanoff/skit/errs"
	"github.com/assanoff/skit/eventbus"
	"github.com/assanoff/skit/order"
	"github.com/assanoff/skit/page"
)

// Errors the store returns: a get, update or delete of a missing widget, and a
// create that collides with an existing one. The domain never sees the dbx
// errors.
var (
	ErrNotFound      = errors.New("widget not found")
	ErrAlreadyExists = errors.New("widget already exists")
)

// Repo is CRUD over widgets. The callback of a transaction gets it too.
type Repo interface {
	Create(ctx context.Context, w Widget) error
	Update(ctx context.Context, w Widget) error
	Delete(ctx context.Context, id uuid.UUID) error
	QueryByID(ctx context.Context, id uuid.UUID) (Widget, error)
	Query(ctx context.Context, filter QueryFilter, by order.By, pg page.Page) ([]Widget, error)
	QueryByCursor(ctx context.Context, filter QueryFilter, cur page.Cursor) (items []Widget, next string, err error)
	Count(ctx context.Context, filter QueryFilter) (int, error)
}

// Scheduler schedules a task — an event, any work after the commit — in the
// transaction of the callback. It is declared here so the domain does not
// import queue.
type Scheduler interface {
	Schedule(ctx context.Context, v any) error
}

// Store is the persistence contract for widgets: CRUD and transactions. The
// Core depends on this interface; the implementation (widgetdb) lives
// elsewhere.
//
// fn may run more than once (a retry after a deadlock): inside it use only r
// and s, keep no state between runs and do not call the Store. A Store built
// over another transaction joins it and does not commit.
type Store interface {
	Repo

	// WithinTran runs fn in one transaction; r is bound to it.
	WithinTran(ctx context.Context, fn func(r Repo) error) error

	// WithinTranTasks is WithinTran plus s, which schedules tasks in the same
	// transaction. Without tasks wired into the store Schedule fails; with the
	// broker off the route of an event drops it, so the example runs without a
	// broker too.
	WithinTranTasks(ctx context.Context, fn func(r Repo, s Scheduler) error) error
}

// Core implements the widget business logic.
type Core struct {
	log   *slog.Logger
	store Store

	// Optional in-process event bus. When set, Create dispatches a synchronous,
	// best-effort widget.created event after the write (the in-process complement
	// to the durable outbox event). nil disables in-process dispatch.
	bus *eventbus.Bus

	m metrics
}

// Option customizes a Core.
type Option func(*Core)

// WithEventBus enables in-process event dispatch: after a widget is created,
// Create publishes a (widget, created) event on the bus for any registered
// in-process consumers (e.g. a webhook notifier). Dispatch is synchronous and
// best-effort — a consumer's failure is logged by the bus but does not roll back
// the write or fail the request. This is independent of the outbox event of
// the store: a Core may use neither, either, or both.
func WithEventBus(bus *eventbus.Bus) Option {
	return func(c *Core) { c.bus = bus }
}

// NewCore constructs a Core.
func NewCore(log *slog.Logger, store Store, opts ...Option) *Core {
	c := &Core{log: log, store: store, m: newMetrics(nil)}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Create validates and persists a new widget and schedules a widget.created
// event in the same transaction (transactional outbox). The wiring decides
// where the event goes: into the queue of events when the broker is on,
// nowhere when it is off.
func (c *Core) Create(ctx context.Context, nw NewWidget) (Widget, error) {
	now := time.Now().UTC()
	w := Widget{
		ID:          uuid.New(),
		Name:        nw.Name,
		Description: nw.Description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := c.insert(ctx, w); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return Widget{}, errs.Newf(errs.AlreadyExists, "widget %q already exists", nw.Name).
				WithMessageID("widget.already_exists").
				WithArgs(map[string]any{"name": nw.Name})
		}
		return Widget{}, errs.New(errs.Internal, err)
	}
	c.m.created.Inc()

	// In-process, synchronous, best-effort notification. Unlike the event above
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

// insert writes the widget and schedules a widget.created event in the same
// transaction (transactional outbox).
func (c *Core) insert(ctx context.Context, w Widget) error {
	// The domain schedules a plain typed event; it knows nothing about the
	// transaction's mechanics, the queue or the transport. s writes it in the
	// same transaction as the widget, so both commit (or roll back) together.
	return c.store.WithinTranTasks(ctx, func(r Repo, s Scheduler) error {
		if err := r.Create(ctx, w); err != nil {
			return err
		}
		return s.Schedule(ctx, Created{
			ID:          w.ID.String(),
			Name:        w.Name,
			Description: w.Description,
			CreatedAt:   w.CreatedAt,
		})
	})
}

// QueryByID returns a widget or a NotFound error.
func (c *Core) QueryByID(ctx context.Context, id uuid.UUID) (Widget, error) {
	w, err := c.store.QueryByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Widget{}, c.notFound(id)
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
		if errors.Is(err, ErrNotFound) {
			return Widget{}, c.notFound(id)
		}
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
		if errors.Is(err, ErrNotFound) {
			return c.notFound(id)
		}
		return errs.New(errs.Internal, err)
	}
	return nil
}

// notFound builds the canonical not-found error for id.
func (c *Core) notFound(id uuid.UUID) error {
	return errs.Newf(errs.NotFound, "widget %s not found", id).
		WithMessageID("widget.not_found").
		WithArgs(map[string]any{"id": id.String()})
}
