package userdb

import (
	"time"
	"uuid"

	"github.com/assanoff/skit/dbx"

	"github.com/assanoff/skit-x/core/user"
)

// dbUser is the database representation of a user. The id is a
// dbx.UUID: the stdlib uuid.UUID has no sql.Scanner / driver.Valuer.
type dbUser struct {
	ID        dbx.UUID  `db:"id"`
	Email     string    `db:"email"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

func toDBUser(u user.User) dbUser {
	return dbUser{
		ID:        dbx.UUID(u.ID),
		Email:     u.Email,
		Name:      u.Name,
		CreatedAt: u.CreatedAt.UTC(),
		UpdatedAt: u.UpdatedAt.UTC(),
	}
}

func toCoreUser(r dbUser) user.User {
	return user.User{
		ID:        uuid.UUID(r.ID),
		Email:     r.Email,
		Name:      r.Name,
		CreatedAt: r.CreatedAt.In(time.UTC),
		UpdatedAt: r.UpdatedAt.In(time.UTC),
	}
}

func toCoreUsers(rows []dbUser) []user.User {
	out := make([]user.User, len(rows))
	for i, r := range rows {
		out[i] = toCoreUser(r)
	}
	return out
}
