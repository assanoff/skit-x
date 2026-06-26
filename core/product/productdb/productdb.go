// Package productdb is the Postgres implementation of product.Store, following
// the SDK pg-store convention (inline const queries, a model.go row type,
// dialect-composed pagination) and the servicekit sqldb helpers.
package productdb

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

	"github.com/assanoff/service-kit-x/core/product"
)

// Store implements product.Store against Postgres.
type Store struct {
	log     *logger.Logger
	db      *sqlx.DB
	dialect dialect.Dialect
}

// Option customizes a Store.
type Option func(*Store)

// WithDialect overrides the SQL dialect used to compose engine-specific SQL
// (pagination). Defaults to dialect.Postgres.
func WithDialect(d dialect.Dialect) Option {
	return func(s *Store) {
		s.dialect = d
	}
}

// NewStore builds a Store over the connection pool.
func NewStore(log *logger.Logger, db *sqlx.DB, opts ...Option) *Store {
	s := &Store{log: log, db: db, dialect: dialect.Postgres{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Compile-time check that Store satisfies the domain contract.
var _ product.Store = (*Store)(nil)

// Create implements product.Store.
func (s *Store) Create(ctx context.Context, p product.Product) error {
	const q = `
		INSERT INTO products (id, name, price, created_at, updated_at)
		VALUES (:id, :name, :price, :created_at, :updated_at)`
	if err := sqldb.NamedExecContext(ctx, s.log, s.db, q, toDBProduct(p)); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	return nil
}

// Update implements product.Store.
func (s *Store) Update(ctx context.Context, p product.Product) error {
	const q = `
		UPDATE products
		SET name = :name, price = :price, updated_at = :updated_at
		WHERE id = :id`
	if err := sqldb.NamedExecContext(ctx, s.log, s.db, q, toDBProduct(p)); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return nil
}

// Delete implements product.Store.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM products WHERE id = :id`
	data := struct {
		ID string `db:"id"`
	}{ID: id.String()}
	if err := sqldb.NamedExecContext(ctx, s.log, s.db, q, data); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// QueryByID implements product.Store.
func (s *Store) QueryByID(ctx context.Context, id uuid.UUID) (product.Product, error) {
	const q = `SELECT id, name, price, created_at, updated_at FROM products WHERE id = :id`
	data := struct {
		ID string `db:"id"`
	}{ID: id.String()}

	var row dbProduct
	if err := sqldb.NamedQueryStruct(ctx, s.log, s.db, q, data, &row); err != nil {
		return product.Product{}, fmt.Errorf("querybyid: %w", err)
	}
	return toCoreProduct(row), nil
}

// Count implements product.Store, honoring filter so a filtered total stays
// consistent with the filtered page.
func (s *Store) Count(ctx context.Context, filter product.QueryFilter) (int, error) {
	data := map[string]any{}
	buf := bytes.NewBufferString(`SELECT count(*) AS n FROM products`)
	s.applyFilter(filter, data, buf)

	var row struct {
		N int `db:"n"`
	}
	if err := sqldb.NamedQueryStruct(ctx, s.log, s.db, buf.String(), data, &row); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return row.N, nil
}

// Query implements product.Store: one filtered, ordered page. The WHERE is built
// by applyFilter, the ORDER BY by orderByClause (allowlisted), and the pagination
// clause via the store's dialect, binding :offset and :rows_per_page below.
func (s *Store) Query(ctx context.Context, filter product.QueryFilter, by order.By, pg page.Page) ([]product.Product, error) {
	clause, err := orderByClause(by)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	data := map[string]any{
		"offset":        pg.Offset(),
		"rows_per_page": pg.RowsPerPage(),
	}

	buf := bytes.NewBufferString(`SELECT id, name, price, created_at, updated_at FROM products`)
	s.applyFilter(filter, data, buf)
	buf.WriteString(clause)
	s.dialect.Paginate(buf)

	var rows []dbProduct
	if err := sqldb.NamedQuerySlice(ctx, s.log, s.db, buf.String(), data, &rows); err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return toCoreProducts(rows), nil
}

// QueryByCursor implements product.Store with keyset (cursor) pagination. It
// decodes the cursor's boundary, fetches one extra row to tell whether a further
// page exists, trims it, and encodes the last returned row's (created_at, id)
// into the next cursor token. The predicate + ORDER BY are an index range scan on
// products_created_at_id_desc_idx, so the query stays O(limit) at any depth.
func (s *Store) QueryByCursor(ctx context.Context, filter product.QueryFilter, cur page.Cursor) ([]product.Product, string, error) {
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
		ts, id, err := decodeProductCursor(key)
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

	buf := bytes.NewBufferString(`SELECT id, name, price, created_at, updated_at FROM products`)
	writeWhere(buf, wc)
	buf.WriteString(` ORDER BY created_at DESC, id DESC LIMIT :limit`)

	var rows []dbProduct
	if err := sqldb.NamedQuerySlice(ctx, s.log, s.db, buf.String(), data, &rows); err != nil {
		return nil, "", fmt.Errorf("querybycursor: %w", err)
	}

	var next string
	if len(rows) > cur.Limit() {
		rows = rows[:cur.Limit()]
		last := rows[len(rows)-1]
		next = page.EncodeCursor(encodeProductCursor(last.CreatedAt, last.ID))
	}
	return toCoreProducts(rows), next, nil
}

// whereConditions returns the SQL predicates for filter, binding each one's named
// params into data — the chocodev/stories db-layer filter convention: optional
// columns become :name predicates joined later under one WHERE. Returning the
// slice (rather than writing the clause itself) lets QueryByCursor append the
// keyset boundary before the WHERE is assembled.
func (s *Store) whereConditions(filter product.QueryFilter, data map[string]any) []string {
	var wc []string

	if filter.Name != nil {
		data["name"] = "%" + *filter.Name + "%"
		wc = append(wc, "name ILIKE :name")
	}
	if filter.MinPrice != nil {
		data["min_price"] = *filter.MinPrice
		wc = append(wc, "price >= :min_price")
	}
	if filter.MaxPrice != nil {
		data["max_price"] = *filter.MaxPrice
		wc = append(wc, "price <= :max_price")
	}
	return wc
}

// applyFilter writes filter's WHERE clause into buf — the offset Query and Count
// path, which have no keyset boundary to combine.
func (s *Store) applyFilter(filter product.QueryFilter, data map[string]any, buf *bytes.Buffer) {
	writeWhere(buf, s.whereConditions(filter, data))
}

// writeWhere appends a "WHERE a AND b AND …" clause to buf when wc is non-empty.
func writeWhere(buf *bytes.Buffer, wc []string) {
	if len(wc) > 0 {
		buf.WriteString(" WHERE ")
		buf.WriteString(strings.Join(wc, " AND "))
	}
}

// encodeProductCursor packs a product's sort key (created_at, id) into the plain
// string that page.EncodeCursor turns into an opaque, URL-safe token.
func encodeProductCursor(createdAt time.Time, id uuid.UUID) string {
	return createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
}

// decodeProductCursor parses the key encodeProductCursor produced back into the
// boundary timestamp and id.
func decodeProductCursor(key string) (time.Time, string, error) {
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
