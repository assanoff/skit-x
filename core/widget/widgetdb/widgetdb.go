// Package widgetdb is the Postgres implementation of widget.Store. It maps
// between the domain Widget and its database representation and uses the
// servicekit sqldb helpers for query logging and error translation.
package widgetdb

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/assanoff/servicekit/logger"
	"github.com/assanoff/servicekit/sqldb"

	"github.com/assanoff/service-kit-x/core/widget"
)

// Store implements widget.Store against Postgres.
type Store struct {
	log *logger.Logger
	db  sqlx.ExtContext
}

// NewStore builds a Store. Pass a *sqlx.DB for pool-backed use.
func NewStore(log *logger.Logger, db *sqlx.DB) *Store {
	return &Store{log: log, db: db}
}

// Compile-time check that Store satisfies the domain contract.
var _ widget.Store = (*Store)(nil)

// WithTx returns a sibling Store whose queries run on tx, so a widget write can
// commit atomically with an outbox event.
func (s *Store) WithTx(tx sqlx.ExtContext) widget.Store {
	return &Store{log: s.log, db: tx}
}

// Create implements widget.Store.
func (s *Store) Create(ctx context.Context, w widget.Widget) error {
	const q = `
		INSERT INTO widgets (id, name, description, created_at, updated_at)
		VALUES (:id, :name, :description, :created_at, :updated_at)`
	if err := sqldb.NamedExecContext(ctx, s.log, s.db, q, toDBWidget(w)); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	return nil
}

// Update implements widget.Store.
func (s *Store) Update(ctx context.Context, w widget.Widget) error {
	const q = `
		UPDATE widgets
		SET name = :name, description = :description, updated_at = :updated_at
		WHERE id = :id`
	if err := sqldb.NamedExecContext(ctx, s.log, s.db, q, toDBWidget(w)); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return nil
}

// Delete implements widget.Store.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM widgets WHERE id = :id`
	data := struct {
		ID string `db:"id"`
	}{ID: id.String()}
	if err := sqldb.NamedExecContext(ctx, s.log, s.db, q, data); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// QueryByID implements widget.Store.
func (s *Store) QueryByID(ctx context.Context, id uuid.UUID) (widget.Widget, error) {
	const q = `SELECT id, name, description, created_at, updated_at FROM widgets WHERE id = :id`
	data := struct {
		ID string `db:"id"`
	}{ID: id.String()}

	var row dbWidget
	if err := sqldb.NamedQueryStruct(ctx, s.log, s.db, q, data, &row); err != nil {
		return widget.Widget{}, fmt.Errorf("querybyid: %w", err)
	}
	return toCoreWidget(row), nil
}

// BulkInsert inserts many widgets in batched multi-row statements, skipping rows
// whose id already exists. The ON CONFLICT DO NOTHING makes the operation
// idempotent, which matters for at-least-once queue delivery: replaying the same
// import batch will not create duplicates or error.
func (s *Store) BulkInsert(ctx context.Context, ws []widget.Widget) error {
	if len(ws) == 0 {
		return nil
	}
	columns := []string{"id", "name", "description", "created_at", "updated_at"}
	values := make([]any, 0, len(ws)*len(columns))
	for _, w := range ws {
		d := toDBWidget(w)
		values = append(values, d.ID, d.Name, d.Description, d.CreatedAt, d.UpdatedAt)
	}
	if err := sqldb.BulkInsert(ctx, s.log, s.db, "widgets", columns, values, "ON CONFLICT (id) DO NOTHING"); err != nil {
		return fmt.Errorf("bulkinsert: %w", err)
	}
	return nil
}

// Query implements widget.Store.
func (s *Store) Query(ctx context.Context) ([]widget.Widget, error) {
	const q = `SELECT id, name, description, created_at, updated_at FROM widgets ORDER BY created_at DESC`

	var rows []dbWidget
	if err := sqldb.QuerySlice(ctx, s.log, s.db, q, &rows); err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return toCoreWidgets(rows), nil
}
