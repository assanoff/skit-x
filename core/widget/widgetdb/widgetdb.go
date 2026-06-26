// Package widgetdb is the Postgres implementation of widget.Store. It maps
// between the domain Widget and its database representation and uses the
// servicekit sqldb helpers for query logging and error translation.
package widgetdb

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/assanoff/servicekit/logger"
	"github.com/assanoff/servicekit/order"
	"github.com/assanoff/servicekit/page"
	"github.com/assanoff/servicekit/sqldb"
	"github.com/assanoff/servicekit/sqldb/dialect"

	"github.com/assanoff/service-kit-x/core/widget"
)

// Store implements widget.Store against Postgres.
type Store struct {
	log     *logger.Logger
	db      sqlx.ExtContext
	dialect dialect.Dialect
}

// Option customizes a Store.
type Option func(*Store)

// WithDialect overrides the SQL dialect used to compose engine-specific SQL
// (pagination). Defaults to dialect.Postgres.
func WithDialect(d dialect.Dialect) Option { return func(s *Store) { s.dialect = d } }

// NewStore builds a Store. Pass a *sqlx.DB for pool-backed use.
func NewStore(log *logger.Logger, db *sqlx.DB, opts ...Option) *Store {
	s := &Store{log: log, db: db, dialect: dialect.Postgres{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Compile-time check that Store satisfies the domain contract.
var _ widget.Store = (*Store)(nil)

// WithTx returns a sibling Store whose queries run on tx, so a widget write can
// commit atomically with an outbox event.
func (s *Store) WithTx(tx sqlx.ExtContext) widget.Store {
	return &Store{log: s.log, db: tx, dialect: s.dialect}
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

// Count implements widget.Store, honoring filter so a filtered total stays
// consistent with the filtered page.
func (s *Store) Count(ctx context.Context, filter widget.QueryFilter) (int, error) {
	data := map[string]any{}
	buf := bytes.NewBufferString(`SELECT count(*) AS n FROM widgets`)
	s.applyFilter(filter, data, buf)

	var row struct {
		N int `db:"n"`
	}
	if err := sqldb.NamedQueryStruct(ctx, s.log, s.db, buf.String(), data, &row); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return row.N, nil
}

// Query implements widget.Store: one filtered, ordered page. The WHERE is built
// by applyFilter, the ORDER BY by orderByClause (allowlisted), and the pagination
// clause via the store's dialect (engine-specific OFFSET/FETCH vs LIMIT/OFFSET
// behind one seam), binding :offset and :rows_per_page below.
func (s *Store) Query(ctx context.Context, filter widget.QueryFilter, by order.By, pg page.Page) ([]widget.Widget, error) {
	clause, err := orderByClause(by)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	data := map[string]any{
		"offset":        pg.Offset(),
		"rows_per_page": pg.RowsPerPage(),
	}

	buf := bytes.NewBufferString(`SELECT id, name, description, created_at, updated_at FROM widgets`)
	s.applyFilter(filter, data, buf)
	buf.WriteString(clause)
	s.dialect.Paginate(buf)

	var rows []dbWidget
	if err := sqldb.NamedQuerySlice(ctx, s.log, s.db, buf.String(), data, &rows); err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return toCoreWidgets(rows), nil
}

// QueryByCursor implements widget.Store with keyset (cursor) pagination, honoring
// filter. It combines filter's predicates with the keyset boundary in one WHERE,
// fetches one extra row to detect a further page, trims it, and encodes the last
// row's (created_at, id) into the next token. The predicate + ORDER BY are an
// index range scan on widgets_created_at_id_desc_idx, so it stays O(limit).
func (s *Store) QueryByCursor(ctx context.Context, filter widget.QueryFilter, cur page.Cursor) ([]widget.Widget, string, error) {
	key, err := cur.Key()
	if err != nil {
		return nil, "", fmt.Errorf("querybycursor: %w", err)
	}

	data := map[string]any{
		"limit": cur.Limit() + 1, // one extra row signals that a next page exists
	}

	// The keyset boundary is just one more predicate alongside filter's: build the
	// filter conditions, then append the boundary, then write them as one WHERE.
	wc := s.whereConditions(filter, data)
	if key != "" {
		ts, id, err := decodeWidgetCursor(key)
		if err != nil {
			return nil, "", fmt.Errorf("querybycursor: %w", err)
		}
		data["after_ts"] = ts
		data["after_id"] = id
		// Rows strictly past the boundary in the (created_at DESC, id DESC) order;
		// the id tiebreaker makes the keyset total so paging never repeats a row.
		// CAST(... AS uuid) — not ::uuid — so sqlx's named-param parser doesn't
		// trip over the `::`.
		wc = append(wc, "(created_at, id) < (:after_ts, CAST(:after_id AS uuid))")
	}

	buf := bytes.NewBufferString(`SELECT id, name, description, created_at, updated_at FROM widgets`)
	writeWhere(buf, wc)
	buf.WriteString(` ORDER BY created_at DESC, id DESC LIMIT :limit`)

	var rows []dbWidget
	if err := sqldb.NamedQuerySlice(ctx, s.log, s.db, buf.String(), data, &rows); err != nil {
		return nil, "", fmt.Errorf("querybycursor: %w", err)
	}

	var next string
	if len(rows) > cur.Limit() {
		rows = rows[:cur.Limit()]
		last := rows[len(rows)-1]
		next = page.EncodeCursor(encodeWidgetCursor(last.CreatedAt, last.ID))
	}
	return toCoreWidgets(rows), next, nil
}

// whereConditions returns the SQL predicates for filter, binding each one's named
// params into data — the chocodev/stories db-layer filter convention: optional
// columns become :name predicates joined later under one WHERE. Returning the
// slice (rather than writing the clause itself) lets QueryByCursor append the
// keyset boundary before the WHERE is assembled.
func (s *Store) whereConditions(filter widget.QueryFilter, data map[string]any) []string {
	var wc []string

	if filter.Name != nil {
		data["name"] = "%" + *filter.Name + "%"
		wc = append(wc, "name ILIKE :name")
	}
	if filter.Description != nil {
		data["description"] = "%" + *filter.Description + "%"
		wc = append(wc, "description ILIKE :description")
	}
	return wc
}

// applyFilter writes filter's WHERE clause into buf — the offset Query and Count
// path, which have no keyset boundary to combine.
func (s *Store) applyFilter(filter widget.QueryFilter, data map[string]any, buf *bytes.Buffer) {
	writeWhere(buf, s.whereConditions(filter, data))
}

// writeWhere appends a "WHERE a AND b AND …" clause to buf when wc is non-empty.
func writeWhere(buf *bytes.Buffer, wc []string) {
	if len(wc) > 0 {
		buf.WriteString(" WHERE ")
		buf.WriteString(strings.Join(wc, " AND "))
	}
}

// encodeWidgetCursor packs a widget's sort key (created_at, id) into the plain
// string that page.EncodeCursor turns into an opaque, URL-safe token.
func encodeWidgetCursor(createdAt time.Time, id uuid.UUID) string {
	return createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
}

// decodeWidgetCursor parses the key encodeWidgetCursor produced back into the
// boundary timestamp and id.
func decodeWidgetCursor(key string) (time.Time, string, error) {
	tsStr, idStr, ok := strings.Cut(key, "|")
	if !ok {
		return time.Time{}, "", fmt.Errorf("malformed cursor key %q", key)
	}
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("malformed cursor timestamp %q: %w", tsStr, err)
	}
	return ts, idStr, nil
}
