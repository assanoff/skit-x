package widget

import (
	"encoding/json"
	"time"

	"github.com/assanoff/skit/query"
	"github.com/assanoff/skit/translation"

	widgetcore "github.com/assanoff/skit-x/core/widget"
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

// TranslateWidgetReq saves a translation of a widget's content into one language.
type TranslateWidgetReq struct {
	Language    string `json:"language" validate:"required,max=10"`
	Name        string `json:"name" validate:"required,max=100"`
	Description string `json:"description" validate:"max=500"`
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

// CountResponse is the cached total widget count.
type CountResponse struct {
	Count int `json:"count"`
}

// Response is the REST representation of a widget. The translate tags mark the
// fields the translationrest middleware translates into the request language;
// Response is both a rest.ResponseEncoder and a translation.Translatable, so the
// middleware can reach and translate it without per-handler code.
type Response struct {
	ID          string    `json:"id" translate:"primary"`
	Name        string    `json:"name" translate:"name"`
	Description string    `json:"description" translate:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// GetTranslationKey implements translation.Translatable. The model name matches
// the widget audit model type so a widget is keyed consistently across features.
func (r *Response) GetTranslationKey() (modelName, keyID string) {
	return widgetcore.AuditModelType, r.ID
}

// Encode implements rest.ResponseEncoder.
func (r *Response) Encode() ([]byte, string, error) {
	b, err := json.Marshal(r)
	return b, "application/json", err
}

// ResponseList is a collection of widget responses. It implements
// translation.TranslatableList so the middleware translates every item in one
// batch query, and rest.ResponseEncoder so it can be returned directly from a handler.
type ResponseList []*Response

// Translatables implements translation.TranslatableList.
func (l ResponseList) Translatables() []translation.Translatable {
	out := make([]translation.Translatable, len(l))
	for i, r := range l {
		out[i] = r
	}
	return out
}

// Encode implements rest.ResponseEncoder.
func (l ResponseList) Encode() ([]byte, string, error) {
	b, err := json.Marshal(l)
	return b, "application/json", err
}

// PagedResponse is a page of widget responses: it embeds the SDK's
// query.Result (items + total + page + rowsPerPage, and its Encode) and also
// implements translation.TranslatableList over the items, so the translationrest
// middleware still localizes each widget before the envelope is encoded.
type PagedResponse struct {
	query.Result[*Response]
}

// Translatables implements translation.TranslatableList.
func (p PagedResponse) Translatables() []translation.Translatable {
	out := make([]translation.Translatable, len(p.Items))
	for i, r := range p.Items {
		out[i] = r
	}
	return out
}

// CursorPagedResponse is a cursor (keyset) page of widget responses: it embeds
// the SDK's query.CursorResult (items + next/prev tokens, and its Encode) and
// implements translation.TranslatableList over the items, so the translationrest
// middleware still localizes each widget before the envelope is encoded — the
// keyset counterpart of PagedResponse.
type CursorPagedResponse struct {
	query.CursorResult[*Response]
}

// Translatables implements translation.TranslatableList.
func (p CursorPagedResponse) Translatables() []translation.Translatable {
	out := make([]translation.Translatable, len(p.Items))
	for i, r := range p.Items {
		out[i] = r
	}
	return out
}

func toResponse(w widgetcore.Widget) *Response {
	return &Response{
		ID:          w.ID.String(),
		Name:        w.Name,
		Description: w.Description,
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
	}
}

func toResponseList(ws []widgetcore.Widget) ResponseList {
	out := make(ResponseList, len(ws))
	for i, w := range ws {
		out[i] = toResponse(w)
	}
	return out
}

// Resource is the JSON:API representation of a widget. Its fields are tagged for
// github.com/hashicorp/jsonapi, which assembles the document — the handler just
// returns to.JSONAPI(resource). It is an alternative DTO to Response for clients
// that speak JSON:API (see the /widgets/jsonapi endpoints).
type Resource struct {
	ID          string    `jsonapi:"primary,widgets"`
	Name        string    `jsonapi:"attr,name"`
	Description string    `jsonapi:"attr,description"`
	CreatedAt   time.Time `jsonapi:"attr,createdAt,iso8601"`
	UpdatedAt   time.Time `jsonapi:"attr,updatedAt,iso8601"`
}

func toResource(w widgetcore.Widget) *Resource {
	return &Resource{
		ID:          w.ID.String(),
		Name:        w.Name,
		Description: w.Description,
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
	}
}

func toResourceList(ws []widgetcore.Widget) []*Resource {
	out := make([]*Resource, len(ws))
	for i, w := range ws {
		out[i] = toResource(w)
	}
	return out
}
