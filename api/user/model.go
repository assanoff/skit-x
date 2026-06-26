package user

import (
	"encoding/json"
	"time"

	usercore "github.com/assanoff/skit-x/core/user"
)

// CreateUserReq is the request body for creating a user.
type CreateUserReq struct {
	Email string `json:"email" validate:"required,email,max=200"`
	Name  string `json:"name" validate:"required,max=100"`
}

// UpdateUserReq is a partial user update; nil fields are left unchanged.
type UpdateUserReq struct {
	Email *string `json:"email" validate:"omitempty,email,max=200"`
	Name  *string `json:"name" validate:"omitempty,max=100"`
}

// Response is the REST representation of a user. It implements rest.ResponseEncoder, so
// a handler returns it directly — the ResponseEncoder seam (plain JSON here; a module
// may swap in JSON:API or protobuf by implementing Encode differently).
type Response struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Encode implements rest.ResponseEncoder.
func (r *Response) Encode() ([]byte, string, error) {
	b, err := json.Marshal(r)
	return b, "application/json", err
}

func toResponse(u usercore.User) *Response {
	return &Response{
		ID:        u.ID.String(),
		Email:     u.Email,
		Name:      u.Name,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func toResponseList(us []usercore.User) []*Response {
	out := make([]*Response, len(us))
	for i, u := range us {
		out[i] = toResponse(u)
	}
	return out
}
