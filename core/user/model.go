package user

import (
	"time"

	"github.com/google/uuid"

	"github.com/assanoff/servicekit/order"
)

// User is the domain entity.
type User struct {
	ID        uuid.UUID
	Email     string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewUser is the data required to create a User.
type NewUser struct {
	Email string
	Name  string
}

// UpdateUser is the data allowed when updating a User. Nil fields are left
// unchanged.
type UpdateUser struct {
	Email *string
	Name  *string
}

// QueryFilter narrows a user listing. Nil fields are not applied, so the zero
// value matches every user. It is honored by both list paths (offset Query and
// cursor QueryByCursor) and by Count, so a filtered total stays consistent with
// the filtered page.
type QueryFilter struct {
	Name  *string // case-insensitive substring match on name
	Email *string // case-insensitive substring match on email
}

// Order-by field names the offset listing accepts (the cursor listing is fixed to
// the keyset order). These are the allowlist keys; the store maps them to columns.
const (
	OrderByCreatedAt = "created_at"
	OrderByName      = "name"
	OrderByEmail     = "email"
)

// SortableFields is the order.Parse allowlist for user listings: each accepted
// ?order_by field maps to itself (the store turns it into a column). A field
// outside this set is rejected, so a client cannot order by an arbitrary column.
var SortableFields = map[string]string{
	OrderByCreatedAt: OrderByCreatedAt,
	OrderByName:      OrderByName,
	OrderByEmail:     OrderByEmail,
}

// DefaultOrder is newest-first, applied when ?order_by is absent.
var DefaultOrder = order.NewBy(OrderByCreatedAt, order.DESC)
