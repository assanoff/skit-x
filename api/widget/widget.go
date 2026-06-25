// Package widget is the REST transport layer for the widget module. Handlers
// are servicekit rest.HandlerFunc values: they decode/validate input, call the
// Core, and return an Encoder (a DTO or an *errs.Error).
package widget

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/assanoff/servicekit/errs"
	"github.com/assanoff/servicekit/page"
	"github.com/assanoff/servicekit/query"
	"github.com/assanoff/servicekit/to"
	"github.com/assanoff/servicekit/translation"
	"github.com/assanoff/servicekit/web/rest"

	widgetcore "github.com/assanoff/service-kit-x/core/widget"
	"github.com/assanoff/service-kit-x/core/widgetimport"
)

// Counter reports a cached widget count. It is satisfied by a poller.Poller[int],
// which refreshes the count in the background so the read is cheap.
type Counter interface {
	Current() int
}

// Handler exposes widget endpoints.
type Handler struct {
	core       *widgetcore.Core
	importer   *widgetimport.Importer
	counter    Counter
	translator *translation.Translator
}

// New builds a Handler.
func New(core *widgetcore.Core, importer *widgetimport.Importer, counter Counter, translator *translation.Translator) *Handler {
	return &Handler{core: core, importer: importer, counter: counter, translator: translator}
}

// Routes registers the widget endpoints through the handle seam, so this
// transport does not depend on the router type — the server's Install function
// passes router.HandleApp. Reads are public; each write passes authMW (e.g.
// JWT auth + RBAC, as rest.MidFunc) so it requires authorization when auth is
// enabled, and is unguarded otherwise.
func (h *Handler) Routes(handle rest.Handle, authMW ...rest.MidFunc) {
	handle("GET /widgets", h.query)
	handle("GET /widgets/count", h.count)
	// JSON:API variants (same data, application/vnd.api+json via to.JSONAPI).
	handle("GET /widgets/jsonapi", h.queryJSONAPI)
	handle("GET /widgets/jsonapi/{id}", h.queryByIDJSONAPI)
	handle("GET /widgets/{id}", h.queryByID)

	handle("POST /widgets", h.create, authMW...)
	handle("POST /widgets/import", h.importBatch, authMW...)
	handle("PUT /widgets/{id}", h.update, authMW...)
	handle("DELETE /widgets/{id}", h.delete, authMW...)
	handle("POST /widgets/{id}/translations", h.saveTranslation, authMW...)
}

// importBatch enqueues a batch of widgets for asynchronous bulk insertion by the
// background worker. It returns 202 Accepted immediately; the widgets appear
// once the worker drains the queue.
//
//	@Summary	Bulk-import widgets (async)
//	@Tags		widgets
//	@Accept		json
//	@Produce	json
//	@Param		request	body		ImportWidgetsReq	true	"batch to import"
//	@Success	202		{object}	ImportResponse
//	@Failure	400		{string}	string	"invalid argument"
//	@Router		/widgets/import [post]
func (h *Handler) importBatch(ctx context.Context, r *http.Request) rest.Encoder {
	var req ImportWidgetsReq
	if err := rest.Decode(r, &req); err != nil {
		return errs.From(err)
	}

	news := make([]widgetcore.NewWidget, len(req.Widgets))
	for i, w := range req.Widgets {
		news[i] = widgetcore.NewWidget{Name: w.Name, Description: w.Description}
	}

	scheduled, err := h.importer.Schedule(ctx, req.Name, news)
	if err != nil {
		return errs.From(err)
	}
	return rest.JSONStatus(ImportResponse{Scheduled: scheduled, Count: len(news)}, http.StatusAccepted)
}

// create creates a single widget.
//
//	@Summary	Create a widget
//	@Tags		widgets
//	@Accept		json
//	@Produce	json
//	@Param		request	body		CreateWidgetReq	true	"widget to create"
//	@Success	201		{object}	Response
//	@Failure	400		{string}	string	"invalid argument"
//	@Router		/widgets [post]
func (h *Handler) create(ctx context.Context, r *http.Request) rest.Encoder {
	var req CreateWidgetReq
	if err := rest.Decode(r, &req); err != nil {
		return errs.From(err)
	}

	w, err := h.core.Create(ctx, widgetcore.NewWidget{Name: req.Name, Description: req.Description})
	if err != nil {
		return errs.From(err)
	}
	return rest.JSONStatus(toResponse(w), http.StatusCreated)
}

// query lists all widgets.
//
//	@Summary	List widgets
//	@Tags		widgets
//	@Produce	json
//	@Success	200	{array}	Response
//	@Router		/widgets [get]
func (h *Handler) query(ctx context.Context, r *http.Request) rest.Encoder {
	pg, err := page.Parse(r.URL.Query().Get("page"), r.URL.Query().Get("rows"))
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	ws, err := h.core.Query(ctx, pg)
	if err != nil {
		return errs.From(err)
	}
	total, err := h.core.Count(ctx)
	if err != nil {
		return errs.From(err)
	}
	// PagedResponse wraps query.Result and implements translation.TranslatableList,
	// so the translationrest middleware still reaches and translates each item
	// before the result (items + total + page) is encoded.
	return PagedResponse{Result: query.NewResult(toResponseList(ws), total, pg)}
}

// count returns the cached total widget count. The value is refreshed in the
// background by a poller, so this read is cheap and never hits the database.
//
//	@Summary	Cached widget count
//	@Tags		widgets
//	@Produce	json
//	@Success	200	{object}	CountResponse
//	@Router		/widgets/count [get]
func (h *Handler) count(_ context.Context, _ *http.Request) rest.Encoder {
	return rest.JSON(CountResponse{Count: h.counter.Current()})
}

// queryByID returns one widget by id.
//
//	@Summary	Get a widget by id
//	@Tags		widgets
//	@Produce	json
//	@Param		id	path		string	true	"widget id"
//	@Success	200	{object}	Response
//	@Failure	404	{string}	string	"not found"
//	@Router		/widgets/{id} [get]
func (h *Handler) queryByID(ctx context.Context, r *http.Request) rest.Encoder {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", r.PathValue("id"))
	}

	w, err := h.core.QueryByID(ctx, id)
	if err != nil {
		return errs.From(err)
	}
	// Returned directly so the translationrest middleware can translate it.
	return toResponse(w)
}

// queryJSONAPI lists widgets as a JSON:API collection. It mirrors query but
// returns the WidgetResource DTO (jsonapi-tagged) via to.JSONAPI, which builds
// the application/vnd.api+json document. Content is canonical (the JSON:API
// encoder is not wired into the per-record translation middleware).
//
//	@Summary	List widgets (JSON:API)
//	@Tags		widgets
//	@Produce	application/vnd.api+json
//	@Router		/widgets/jsonapi [get]
func (h *Handler) queryJSONAPI(ctx context.Context, r *http.Request) rest.Encoder {
	pg, err := page.Parse(r.URL.Query().Get("page"), r.URL.Query().Get("rows"))
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	ws, err := h.core.Query(ctx, pg)
	if err != nil {
		return errs.From(err)
	}
	return to.JSONAPI(toResourceList(ws))
}

// queryByIDJSONAPI returns a single widget as a JSON:API resource.
//
//	@Summary	Get a widget (JSON:API)
//	@Tags		widgets
//	@Produce	application/vnd.api+json
//	@Param		id	path	string	true	"Widget ID"
//	@Router		/widgets/jsonapi/{id} [get]
func (h *Handler) queryByIDJSONAPI(ctx context.Context, r *http.Request) rest.Encoder {
	id, err := uuid.Parse(rest.Param(r, "id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", rest.Param(r, "id"))
	}

	w, err := h.core.QueryByID(ctx, id)
	if err != nil {
		return errs.From(err)
	}
	return to.JSONAPI(toResource(w))
}

// update applies a partial update to a widget.
//
//	@Summary	Update a widget
//	@Tags		widgets
//	@Accept		json
//	@Produce	json
//	@Param		id		path		string			true	"widget id"
//	@Param		request	body		UpdateWidgetReq	true	"fields to update"
//	@Success	200		{object}	Response
//	@Failure	400		{string}	string	"invalid argument"
//	@Failure	404		{string}	string	"not found"
//	@Router		/widgets/{id} [put]
func (h *Handler) update(ctx context.Context, r *http.Request) rest.Encoder {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", r.PathValue("id"))
	}

	var req UpdateWidgetReq
	if err := rest.Decode(r, &req); err != nil {
		return errs.From(err)
	}

	w, err := h.core.Update(ctx, id, widgetcore.UpdateWidget{Name: req.Name, Description: req.Description})
	if err != nil {
		return errs.From(err)
	}
	return rest.JSON(toResponse(w))
}

// delete removes a widget.
//
//	@Summary	Delete a widget
//	@Tags		widgets
//	@Param		id	path	string	true	"widget id"
//	@Success	204	"no content"
//	@Failure	404	{string}	string	"not found"
//	@Router		/widgets/{id} [delete]
func (h *Handler) delete(ctx context.Context, r *http.Request) rest.Encoder {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", r.PathValue("id"))
	}

	if err := h.core.Delete(ctx, id); err != nil {
		return errs.From(err)
	}
	return nil // 204 No Content
}

// saveTranslation stores a translation of a widget's content for one language.
// The Response DTO carries the translate tags, so saving is one Save call; the
// translationrest middleware later applies it to read responses automatically.
//
//	@Summary	Save a widget translation
//	@Tags		widgets
//	@Accept		json
//	@Produce	json
//	@Param		id		path	string				true	"widget id"
//	@Param		request	body	TranslateWidgetReq	true	"translation to save"
//	@Success	200	{object}	Response
//	@Failure	400	{string}	string	"invalid argument"
//	@Router		/widgets/{id}/translations [post]
func (h *Handler) saveTranslation(ctx context.Context, r *http.Request) rest.Encoder {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", r.PathValue("id"))
	}

	var req TranslateWidgetReq
	if err := rest.Decode(r, &req); err != nil {
		return errs.From(err)
	}

	lang := translation.Language{Code: req.Language}
	if err := h.translator.ValidateLanguage(lang); err != nil {
		return errs.Newf(errs.InvalidArgument, "unsupported language %q", req.Language)
	}

	tr := &Response{ID: id.String(), Name: req.Name, Description: req.Description}
	if err := h.translator.Save(ctx, lang, tr); err != nil {
		return errs.From(err)
	}
	return tr
}
