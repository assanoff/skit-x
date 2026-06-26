package product

import (
	"time"

	"github.com/google/uuid"

	"github.com/assanoff/skit/order"
)

// Product is the domain entity. Price is stored as an integer minor unit (e.g.
// cents) to avoid floating-point money.
type Product struct {
	ID        uuid.UUID
	Name      string
	Price     int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewProduct is the data required to create a Product.
type NewProduct struct {
	Name  string
	Price int64
}

// UpdateProduct is the data allowed when updating a Product. Nil fields are left
// unchanged.
type UpdateProduct struct {
	Name  *string
	Price *int64
}

// QueryFilter narrows a product listing. Nil fields are not applied, so the zero
// value matches every product. It is honored by both list paths (offset Query
// and cursor QueryByCursor) and by Count, so a filtered total stays consistent
// with the filtered page.
type QueryFilter struct {
	Name     *string // case-insensitive substring match on name
	MinPrice *int64  // inclusive lower bound on price
	MaxPrice *int64  // inclusive upper bound on price
}

// Order-by field names the offset listing accepts (the cursor listing is fixed to
// the keyset order). These are the allowlist keys; the store maps them to columns.
const (
	OrderByCreatedAt = "created_at"
	OrderByName      = "name"
	OrderByPrice     = "price"
)

// SortableFields is the order.Parse allowlist for product listings: each accepted
// ?order_by field maps to itself (the store turns it into a column). A field
// outside this set is rejected, so a client cannot order by an arbitrary column.
var SortableFields = map[string]string{
	OrderByCreatedAt: OrderByCreatedAt,
	OrderByName:      OrderByName,
	OrderByPrice:     OrderByPrice,
}

// DefaultOrder is newest-first, applied when ?order_by is absent.
var DefaultOrder = order.NewBy(OrderByCreatedAt, order.DESC)
