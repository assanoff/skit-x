package widget

import (
	"time"

	widgetcore "github.com/assanoff/service-kit-x/core/widget"
)

// CreateWidgetReq is the request body for creating a single widget.
type CreateWidgetReq struct {
	Name        string `json:"name" validate:"required,max=100"`
	Description string `json:"description" validate:"max=500"`
}

// UpdateWidgetReq is the request body for a partial widget update; nil fields
// are left unchanged.
type UpdateWidgetReq struct {
	Name        *string `json:"name" validate:"omitempty,max=100"`
	Description *string `json:"description" validate:"omitempty,max=500"`
}

// ImportWidgetsReq is a batch enqueued for asynchronous bulk import. Name is an
// optional dedup key: re-posting the same name is a no-op.
type ImportWidgetsReq struct {
	Name    string            `json:"name" validate:"max=200"`
	Widgets []CreateWidgetReq `json:"widgets" validate:"required,min=1,max=1000,dive"`
}

// ImportResponse acknowledges an accepted bulk-import batch.
type ImportResponse struct {
	Scheduled bool `json:"scheduled"`
	Count     int  `json:"count"`
}

// Response is the REST representation of a widget.
type Response struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toResponse(w widgetcore.Widget) Response {
	return Response{
		ID:          w.ID.String(),
		Name:        w.Name,
		Description: w.Description,
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
	}
}

func toResponses(ws []widgetcore.Widget) []Response {
	out := make([]Response, len(ws))
	for i, w := range ws {
		out[i] = toResponse(w)
	}
	return out
}
