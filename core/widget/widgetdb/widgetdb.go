// Package widgetdb is the Postgres implementation of widget.Store. It maps
// between the domain Widget and its database representation and uses the
// skit dbx helpers for query logging and error translation.
package widgetdb

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"uuid"

	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/order"
	"github.com/assanoff/skit/page"

	"github.com/assanoff/skit-x/core/widget"
)

// Store implements widget.Store against Postgres.
type Store struct {
	log *slog.Logger
	db  dbx.Querier
}

// NewStore builds a Store. Pass a *dbx.DB for pool-backed use.
func NewStore(log *slog.Logger, db *dbx.DB) *Store {
	return &Store{log: log, db: db}
}

// Compile-time check that Store satisfies the domain contract.
var _ widget.Store = (*Store)(nil)

// WithTx returns a sibling Store whose queries run on tx, so a widget write can
// commit atomically with an outbox event.
func (s *Store) WithTx(tx dbx.Querier) widget.Store {
	return &Store{log: s.log, db: tx}
}

// Create implements widget.Store.
func (s *Store) Create(ctx context.Context, w widget.Widget) error {
	const q = `
		INSERT INTO widgets (id, name, description, created_at, updated_at)
		VALUES (:id, :name, :description, :created_at, :updated_at)`
	if err := dbx.NamedExec(ctx, s.db, q, toDBWidget(w)); err != nil {
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
	n, err := dbx.NamedExecRowsAffected(ctx, s.db, q, toDBWidget(w))
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update: %w", dbx.ErrDBNotFound)
	}
	return nil
}

// Delete implements widget.Store.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM widgets WHERE id = :id`
	data := struct {
		ID string `db:"id"`
	}{ID: id.String()}
	n, err := dbx.NamedExecRowsAffected(ctx, s.db, q, data)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("delete: %w", dbx.ErrDBNotFound)
	}
	return nil
}

// QueryByID implements widget.Store.
func (s *Store) QueryByID(ctx context.Context, id uuid.UUID) (widget.Widget, error) {
	const q = `SELECT id, name, description, created_at, updated_at FROM widgets WHERE id = :id`
	data := struct {
		ID string `db:"id"`
	}{ID: id.String()}

	row, err := dbx.NamedQueryOne[dbWidget](ctx, s.db, q, data)
	if err != nil {
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
	if err := dbx.BulkInsert(ctx, s.db, "widgets", columns, values, "ON CONFLICT (id) DO NOTHING"); err != nil {
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

	n, err := dbx.NamedQueryOne[int](ctx, s.db, buf.String(), data)
	if err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// Query implements widget.Store: one filtered, ordered page. The WHERE is built
// by applyFilter, the ORDER BY by orderByClause (allowlisted), and the pagination
// clause binds :offset and :rows_per_page below.
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
	buf.WriteString(" OFFSET :offset ROWS FETCH NEXT :rows_per_page ROWS ONLY")

	rows, err := dbx.NamedQueryList[dbWidget](ctx, s.db, buf.String(), data)
	if err != nil {
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
		ts, id, decodeErr := decodeWidgetCursor(key)
		if decodeErr != nil {
			return nil, "", fmt.Errorf("querybycursor: %w", decodeErr)
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

	rows, err := dbx.NamedQueryList[dbWidget](ctx, s.db, buf.String(), data)
	if err != nil {
		return nil, "", fmt.Errorf("querybycursor: %w", err)
	}

	var next string
	if len(rows) > cur.Limit() {
		rows = rows[:cur.Limit()]
		last := rows[len(rows)-1]
		next = page.EncodeCursor(encodeWidgetCursor(last.CreatedAt, uuid.UUID(last.ID)))
	}
	return toCoreWidgets(rows), next, nil
}

// whereConditions returns the SQL predicates for filter, binding each one's named
// params into data — the db-layer filter convention: optional columns become
// :name predicates joined later under one WHERE. Returning the slice (rather than
// writing the clause itself) lets QueryByCursor append the keyset boundary before
// the WHERE is assembled.
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
