// Package user is an example business module: a clean REST CRUD vertical (no
// outbox/eventbus/audit) that demonstrates the skit web architecture. The
// Core holds business logic and depends only on the Store interface declared
// here; the concrete Postgres implementation lives in the nested userdb package,
// keeping the domain testable and storage-agnostic.
package user

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

// Store is the persistence contract for users. The Core depends on this
// interface; concrete implementations (e.g. userdb) live elsewhere.
type Store interface {
	Create(ctx context.Context, u User) error
	Update(ctx context.Context, u User) error
	Delete(ctx context.Context, id uuid.UUID) error
	QueryByID(ctx context.Context, id uuid.UUID) (User, error)
	Query(ctx context.Context, filter QueryFilter, by order.By, pg page.Page) ([]User, error)
	QueryByCursor(ctx context.Context, filter QueryFilter, cur page.Cursor) (items []User, next string, err error)
	Count(ctx context.Context, filter QueryFilter) (int, error)
}

// Core implements the user business logic.
type Core struct {
	log   *logger.Logger
	store Store
}

// NewCore constructs a Core.
func NewCore(log *logger.Logger, store Store) *Core {
	return &Core{log: log, store: store}
}

// Create validates and persists a new user. A duplicate email maps to
// AlreadyExists (the users table has a unique email index).
func (c *Core) Create(ctx context.Context, nu NewUser) (User, error) {
	now := time.Now().UTC()
	u := User{
		ID:        uuid.New(),
		Email:     nu.Email,
		Name:      nu.Name,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := c.store.Create(ctx, u); err != nil {
		return User{}, c.writeErr(err, u.Email)
	}
	return u, nil
}

// QueryByID returns a user or a NotFound error.
func (c *Core) QueryByID(ctx context.Context, id uuid.UUID) (User, error) {
	u, err := c.store.QueryByID(ctx, id)
	if err != nil {
		if errors.Is(err, dbx.ErrDBNotFound) {
			return User{}, c.notFound(id)
		}
		return User{}, errs.New(errs.Internal, err)
	}
	return u, nil
}

// Query returns one page of users matching filter, ordered by.
func (c *Core) Query(ctx context.Context, filter QueryFilter, by order.By, pg page.Page) ([]User, error) {
	us, err := c.store.Query(ctx, filter, by, pg)
	if err != nil {
		return nil, errs.New(errs.Internal, err)
	}
	return us, nil
}

// QueryByCursor returns up to cur.Limit() users matching filter, newest-first,
// starting after the keyset boundary the cursor encodes (the first page when its
// token is empty), plus an opaque next-page cursor token — empty when there are
// no more rows. Unlike Query (offset), it is stable under concurrent inserts and
// stays cheap at any depth. Prev paging is not offered here (forward-only feed).
func (c *Core) QueryByCursor(ctx context.Context, filter QueryFilter, cur page.Cursor) ([]User, string, error) {
	us, next, err := c.store.QueryByCursor(ctx, filter, cur)
	if err != nil {
		return nil, "", errs.New(errs.Internal, err)
	}
	return us, next, nil
}

// Count returns the number of users matching filter.
func (c *Core) Count(ctx context.Context, filter QueryFilter) (int, error) {
	n, err := c.store.Count(ctx, filter)
	if err != nil {
		return 0, errs.New(errs.Internal, err)
	}
	return n, nil
}

// Update applies a partial update and persists it.
func (c *Core) Update(ctx context.Context, id uuid.UUID, uu UpdateUser) (User, error) {
	u, err := c.QueryByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	if uu.Email != nil {
		u.Email = *uu.Email
	}
	if uu.Name != nil {
		u.Name = *uu.Name
	}
	u.UpdatedAt = time.Now().UTC()

	if err := c.store.Update(ctx, u); err != nil {
		return User{}, c.writeErr(err, u.Email)
	}
	return u, nil
}

// Delete removes a user.
func (c *Core) Delete(ctx context.Context, id uuid.UUID) error {
	if err := c.store.Delete(ctx, id); err != nil {
		if errors.Is(err, dbx.ErrDBNotFound) {
			return c.notFound(id)
		}
		return errs.New(errs.Internal, err)
	}
	return nil
}

// writeErr maps a store write error to the right domain error.
func (c *Core) writeErr(err error, email string) error {
	if errors.Is(err, dbx.ErrDBDuplicatedEntry) {
		return errs.Newf(errs.AlreadyExists, "user %q already exists", email).
			WithMessageID("user.already_exists").
			WithArgs(map[string]any{"email": email})
	}
	return errs.New(errs.Internal, err)
}

// notFound builds the canonical not-found error for id.
func (c *Core) notFound(id uuid.UUID) error {
	return errs.Newf(errs.NotFound, "user %s not found", id).
		WithMessageID("user.not_found").
		WithArgs(map[string]any{"id": id.String()})
}
