package productdb

import (
	"time"

	"github.com/google/uuid"

	"github.com/assanoff/skit-x/core/product"
)

// dbProduct is the database representation of a product.
type dbProduct struct {
	ID        uuid.UUID `db:"id"`
	Name      string    `db:"name"`
	Price     int64     `db:"price"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

func toDBProduct(p product.Product) dbProduct {
	return dbProduct{
		ID:        p.ID,
		Name:      p.Name,
		Price:     p.Price,
		CreatedAt: p.CreatedAt.UTC(),
		UpdatedAt: p.UpdatedAt.UTC(),
	}
}

func toCoreProduct(r dbProduct) product.Product {
	return product.Product{
		ID:        r.ID,
		Name:      r.Name,
		Price:     r.Price,
		CreatedAt: r.CreatedAt.In(time.UTC),
		UpdatedAt: r.UpdatedAt.In(time.UTC),
	}
}

func toCoreProducts(rows []dbProduct) []product.Product {
	out := make([]product.Product, len(rows))
	for i, r := range rows {
		out[i] = toCoreProduct(r)
	}
	return out
}
