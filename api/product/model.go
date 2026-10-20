package product

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/assanoff/skit/validate"

	productcore "github.com/assanoff/skit-x/core/product"
)

// CreateProductReq is the request body for creating a product.
type CreateProductReq struct {
	Name  string `json:"name" validate:"required,max=200"`
	Price int64  `json:"price" validate:"gte=0"`
}

// Decode implements rest.Decoder.
func (req *CreateProductReq) Decode(data []byte) error {
	if err := json.Unmarshal(data, req); err != nil {
		return fmt.Errorf("decode create product: %w", err)
	}
	return nil
}

// Validate validates the request via its struct tags.
func (req *CreateProductReq) Validate() error {
	return validate.Check(req)
}

// UpdateProductReq is a partial product update; nil fields are left unchanged.
type UpdateProductReq struct {
	Name  *string `json:"name" validate:"omitempty,max=200"`
	Price *int64  `json:"price" validate:"omitempty,gte=0"`
}

// Decode implements rest.Decoder.
func (req *UpdateProductReq) Decode(data []byte) error {
	if err := json.Unmarshal(data, req); err != nil {
		return fmt.Errorf("decode update product: %w", err)
	}
	return nil
}

// Validate validates the request via its struct tags.
func (req *UpdateProductReq) Validate() error {
	return validate.Check(req)
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
	data, err := json.Marshal(r)
	if err != nil {
		return nil, "", fmt.Errorf("encode product: %w", err)
	}
	return data, "application/json", nil
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
