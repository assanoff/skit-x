// Package product is an example business module: a clean REST CRUD vertical that
// demonstrates the skit web architecture. The Core holds business logic
// and depends only on the Store interface declared here; the Postgres
// implementation lives in the nested productdb package.
package product

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/errs"
	"github.com/assanoff/skit/logger"
	"github.com/assanoff/skit/order"
	"github.com/assanoff/skit/page"
)

// Store is the persistence contract for products.
type Store interface {
	Create(ctx context.Context, p Product) error
	Update(ctx context.Context, p Product) error
	Delete(ctx context.Context, id uuid.UUID) error
	QueryByID(ctx context.Context, id uuid.UUID) (Product, error)
	Query(ctx context.Context, filter QueryFilter, by order.By, pg page.Page) ([]Product, error)
	QueryByCursor(ctx context.Context, filter QueryFilter, cur page.Cursor) (items []Product, next string, err error)
	Count(ctx context.Context, filter QueryFilter) (int, error)
}

// Core implements the product business logic.
type Core struct {
	log   *logger.Logger
	store Store
}

// NewCore constructs a Core.
func NewCore(log *logger.Logger, store Store) *Core {
	return &Core{log: log, store: store}
}

// Create validates and persists a new product.
func (c *Core) Create(ctx context.Context, np NewProduct) (Product, error) {
	now := time.Now().UTC()
	p := Product{
		ID:        uuid.New(),
		Name:      np.Name,
		Price:     np.Price,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := c.store.Create(ctx, p); err != nil {
		return Product{}, errs.New(errs.Internal, err)
	}
	return p, nil
}

// QueryByID returns a product or a NotFound error.
func (c *Core) QueryByID(ctx context.Context, id uuid.UUID) (Product, error) {
	p, err := c.store.QueryByID(ctx, id)
	if err != nil {
		if errors.Is(err, dbx.ErrDBNotFound) {
			return Product{}, c.notFound(id)
		}
		return Product{}, errs.New(errs.Internal, err)
	}
	return p, nil
}

// Query returns one page of products matching filter, ordered by.
func (c *Core) Query(ctx context.Context, filter QueryFilter, by order.By, pg page.Page) ([]Product, error) {
	ps, err := c.store.Query(ctx, filter, by, pg)
	if err != nil {
		return nil, errs.New(errs.Internal, err)
	}
	return ps, nil
}

// QueryByCursor returns up to cur.Limit() products matching filter, newest-first,
// starting after the keyset boundary the cursor encodes (the first page when its
// token is empty), plus an opaque next-page cursor token — empty when there are
// no more rows. Unlike Query (offset), it is stable under concurrent inserts and
// stays cheap at any depth. Prev paging is not offered here (forward-only feed).
func (c *Core) QueryByCursor(ctx context.Context, filter QueryFilter, cur page.Cursor) ([]Product, string, error) {
	ps, next, err := c.store.QueryByCursor(ctx, filter, cur)
	if err != nil {
		return nil, "", errs.New(errs.Internal, err)
	}
	return ps, next, nil
}

// Count returns the number of products matching filter.
func (c *Core) Count(ctx context.Context, filter QueryFilter) (int, error) {
	n, err := c.store.Count(ctx, filter)
	if err != nil {
		return 0, errs.New(errs.Internal, err)
	}
	return n, nil
}

// Update applies a partial update and persists it.
func (c *Core) Update(ctx context.Context, id uuid.UUID, up UpdateProduct) (Product, error) {
	p, err := c.QueryByID(ctx, id)
	if err != nil {
		return Product{}, err
	}
	if up.Name != nil {
		p.Name = *up.Name
	}
	if up.Price != nil {
		p.Price = *up.Price
	}
	p.UpdatedAt = time.Now().UTC()

	if err := c.store.Update(ctx, p); err != nil {
		return Product{}, errs.New(errs.Internal, err)
	}
	return p, nil
}

// Delete removes a product.
func (c *Core) Delete(ctx context.Context, id uuid.UUID) error {
	if err := c.store.Delete(ctx, id); err != nil {
		if errors.Is(err, dbx.ErrDBNotFound) {
			return c.notFound(id)
		}
		return errs.New(errs.Internal, err)
	}
	return nil
}

// notFound builds the canonical not-found error for id.
func (c *Core) notFound(id uuid.UUID) error {
	return errs.Newf(errs.NotFound, "product %s not found", id).
		WithMessageID("product.not_found").
		WithArgs(map[string]any{"id": id.String()})
}
