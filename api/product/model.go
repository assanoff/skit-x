package product

import (
	"encoding/json"
	"time"

	productcore "github.com/assanoff/skit-x/core/product"
)

// CreateProductReq is the request body for creating a product.
type CreateProductReq struct {
	Name  string `json:"name" validate:"required,max=200"`
	Price int64  `json:"price" validate:"gte=0"`
}

// UpdateProductReq is a partial product update; nil fields are left unchanged.
type UpdateProductReq struct {
	Name  *string `json:"name" validate:"omitempty,max=200"`
	Price *int64  `json:"price" validate:"omitempty,gte=0"`
}

// Response is the REST representation of a product. It implements rest.ResponseEncoder
// (the ResponseEncoder seam), so a handler returns it directly.
type Response struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Price     int64     `json:"price"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Encode implements rest.ResponseEncoder.
func (r *Response) Encode() ([]byte, string, error) {
	b, err := json.Marshal(r)
	return b, "application/json", err
}

func toResponse(p productcore.Product) *Response {
	return &Response{
		ID:        p.ID.String(),
		Name:      p.Name,
		Price:     p.Price,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

func toResponseList(ps []productcore.Product) []*Response {
	out := make([]*Response, len(ps))
	for i, p := range ps {
		out[i] = toResponse(p)
	}
	return out
}
