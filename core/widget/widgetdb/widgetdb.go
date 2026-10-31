// Package widgetdb is the Postgres implementation of widget.Store. It maps
// between the domain Widget and its database representation and uses the
// skit dbx helpers for queries. It turns the dbx errors into the widget
// sentinels, so the domain never imports dbx.
package widgetdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"uuid"

	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/order"
	"github.com/assanoff/skit/outbox"
	"github.com/assanoff/skit/page"

	"github.com/assanoff/skit-x/core/widget"
)

// repo is CRUD over the pool or a transaction.
type repo struct {
	log *slog.Logger
	q   dbx.Querier // *dbx.DB or *dbx.Tx
}

// withTx returns a copy of r bound to tx.
func (r *repo) withTx(tx *dbx.Tx) *repo {
	c := *r
	c.q = tx
	return &c
}

// Option configures a Store.
type Option func(*Store)

// WithEvents turns events on: WithinTranEvents hands fn the publisher fn(tx)
// bound to the transaction. Without it the events are dropped.
func WithEvents(fn func(tx *dbx.Tx) *outbox.Publisher) Option {
	return func(s *Store) { s.events = fn }
}

// Store implements widget.Store against Postgres. Over a pool its WithinTran
// opens a transaction, over a transaction it joins it.
type Store struct {
	*repo

	events func(tx *dbx.Tx) *outbox.Publisher
}

// NewStore builds a Store over q: a pool (*dbx.DB) or a transaction
// (*dbx.Tx). The same constructor binds the store to a transaction.
func NewStore(log *slog.Logger, q dbx.Querier, opts ...Option) *Store {
	s := &Store{repo: &repo{log: log, q: q}, events: discardEvents}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Compile-time check that Store satisfies the domain contract.
var _ widget.Store = (*Store)(nil)

// WithinTran implements widget.Store.
func (s *Store) WithinTran(ctx context.Context, fn func(r widget.Repo) error) error {
	return dbx.WithinTran(ctx, s.log, s.q, func(tx *dbx.Tx) error {
		return fn(s.withTx(tx))
	})
}

// WithinTranEvents implements widget.Store.
func (s *Store) WithinTranEvents(ctx context.Context, fn func(r widget.Repo, pub widget.Publisher) error) error {
	return dbx.WithinTran(ctx, s.log, s.q, func(tx *dbx.Tx) error {
		return fn(s.withTx(tx), s.events(tx))
	})
}

// discardEvents is the store without events: the write goes through, the event
// is dropped.
func discardEvents(*dbx.Tx) *outbox.Publisher { return outbox.Discard() }

// Create implements widget.Repo.
func (r *repo) Create(ctx context.Context, w widget.Widget) error {
	const q = `
		INSERT INTO widgets (id, name, description, created_at, updated_at)
		VALUES (:id, :name, :description, :created_at, :updated_at)`
	err := dbx.NamedExec(ctx, r.q, q, toDBWidget(w))
	if errors.Is(err, dbx.ErrDBDuplicatedEntry) {
		return fmt.Errorf("create: %w: %w", widget.ErrAlreadyExists, err)
	}
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	return nil
}

// Update implements widget.Repo.
func (r *repo) Update(ctx context.Context, w widget.Widget) error {
	const q = `
		UPDATE widgets
		SET name = :name, description = :description, updated_at = :updated_at
		WHERE id = :id`
	n, err := dbx.NamedExecRowsAffected(ctx, r.q, q, toDBWidget(w))
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update: %w", widget.ErrNotFound)
	}
	return nil
}

// Delete implements widget.Repo.
func (r *repo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM widgets WHERE id = :id`
	data := struct {
		ID string `db:"id"`
	}{ID: id.String()}
	n, err := dbx.NamedExecRowsAffected(ctx, r.q, q, data)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("delete: %w", widget.ErrNotFound)
	}
	return nil
}

// QueryByID implements widget.Repo.
func (r *repo) QueryByID(ctx context.Context, id uuid.UUID) (widget.Widget, error) {
	const q = `SELECT id, name, description, created_at, updated_at FROM widgets WHERE id = :id`
	data := struct {
		ID string `db:"id"`
	}{ID: id.String()}

	row, err := dbx.NamedQueryOne[dbWidget](ctx, r.q, q, data)
	if errors.Is(err, dbx.ErrDBNotFound) {
		return widget.Widget{}, fmt.Errorf("querybyid: %w", widget.ErrNotFound)
	}
	if err != nil {
		return widget.Widget{}, fmt.Errorf("querybyid: %w", err)
	}
	return toCoreWidget(row), nil
}

// BulkInsert inserts many widgets in batched multi-row statements, skipping rows
// whose id already exists. The ON CONFLICT DO NOTHING makes the operation
// idempotent, which matters for at-least-once queue delivery: replaying the same
// import batch will not create duplicates or error.
func (r *repo) BulkInsert(ctx context.Context, ws []widget.Widget) error {
	if len(ws) == 0 {
		return nil
	}
	columns := []string{"id", "name", "description", "created_at", "updated_at"}
	values := make([]any, 0, len(ws)*len(columns))
	for _, w := range ws {
		d := toDBWidget(w)
		values = append(values, d.ID, d.Name, d.Description, d.CreatedAt, d.UpdatedAt)
	}
	if err := dbx.BulkInsert(ctx, r.q, "widgets", columns, values, "ON CONFLICT (id) DO NOTHING"); err != nil {
		return fmt.Errorf("bulkinsert: %w", err)
	}
	return nil
}

// Count implements widget.Repo, honoring filter so a filtered total stays
// consistent with the filtered page.
func (r *repo) Count(ctx context.Context, filter widget.QueryFilter) (int, error) {
	data := map[string]any{}
	buf := bytes.NewBufferString(`SELECT count(*) AS n FROM widgets`)
	r.applyFilter(filter, data, buf)

	n, err := dbx.NamedQueryOne[int](ctx, r.q, buf.String(), data)
	if err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// Query implements widget.Repo: one filtered, ordered page. The WHERE is built
// by applyFilter, the ORDER BY by orderByClause (allowlisted), and the pagination
// clause binds :offset and :rows_per_page below.
func (r *repo) Query(ctx context.Context, filter widget.QueryFilter, by order.By, pg page.Page) ([]widget.Widget, error) {
	clause, err := orderByClause(by)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	data := map[string]any{
		"offset":        pg.Offset(),
		"rows_per_page": pg.RowsPerPage(),
	}

	buf := bytes.NewBufferString(`SELECT id, name, description, created_at, updated_at FROM widgets`)
	r.applyFilter(filter, data, buf)
	buf.WriteString(clause)
	buf.WriteString(" OFFSET :offset ROWS FETCH NEXT :rows_per_page ROWS ONLY")

	rows, err := dbx.NamedQueryList[dbWidget](ctx, r.q, buf.String(), data)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return toCoreWidgets(rows), nil
}

// QueryByCursor implements widget.Repo with keyset (cursor) pagination, honoring
// filter. It combines filter's predicates with the keyset boundary in one WHERE,
// fetches one extra row to detect a further page, trims it, and encodes the last
// row's (created_at, id) into the next token. The predicate + ORDER BY are an
// index range scan on widgets_created_at_id_desc_idx, so it stays O(limit).
func (r *repo) QueryByCursor(ctx context.Context, filter widget.QueryFilter, cur page.Cursor) ([]widget.Widget, string, error) {
	key, err := cur.Key()
	if err != nil {
		return nil, "", fmt.Errorf("querybycursor: %w", err)
	}

	data := map[string]any{
		"limit": cur.Limit() + 1, // one extra row signals that a next page exists
	}

	// The keyset boundary is just one more predicate alongside filter's: build the
	// filter conditions, then append the boundary, then write them as one WHERE.
	wc := r.whereConditions(filter, data)
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

	rows, err := dbx.NamedQueryList[dbWidget](ctx, r.q, buf.String(), data)
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
func (r *repo) whereConditions(filter widget.QueryFilter, data map[string]any) []string {
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
func (r *repo) applyFilter(filter widget.QueryFilter, data map[string]any, buf *bytes.Buffer) {
	writeWhere(buf, r.whereConditions(filter, data))
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
