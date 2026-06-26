package userdb

import (
	"fmt"

	"github.com/assanoff/skit/order"

	"github.com/assanoff/skit-x/core/user"
)

// orderByFields maps user's allowlisted order-by field names to SQL columns — the
// db-layer allowlist (mirrors chocodev/stories internal/storygroup/db/order.go).
// Only a column reachable through this map can land in ORDER BY, so a client
// cannot inject an arbitrary column even though the field name is interpolated.
var orderByFields = map[string]string{
	user.OrderByCreatedAt: "created_at",
	user.OrderByName:      "name",
	user.OrderByEmail:     "email",
}

// orderByClause builds the ORDER BY for by, validating its field against
// orderByFields (by.Field is already allowlisted by order.Parse, but the store is
// the authority on which columns exist). The id tiebreaker keeps the order total,
// so paging is deterministic.
func orderByClause(by order.By) (string, error) {
	col, ok := orderByFields[by.Field]
	if !ok {
		return "", fmt.Errorf("order: unknown field %q", by.Field)
	}
	return " ORDER BY " + col + " " + by.Direction + ", id " + by.Direction, nil
}
