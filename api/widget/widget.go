// Package widget is the REST transport layer for the widget module. Handlers
// are skit rest.HandlerFunc values: they decode/validate input, call the
// Core, and return a ResponseEncoder (a DTO or an *errs.Error).
package widget

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/assanoff/skit/auth"
	"github.com/assanoff/skit/errs"
	"github.com/assanoff/skit/order"
	"github.com/assanoff/skit/page"
	"github.com/assanoff/skit/query"
	"github.com/assanoff/skit/rest"
	"github.com/assanoff/skit/to"
	"github.com/assanoff/skit/translation"

	widgetcore "github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widgetimport"
)

// Counter reports a cached widget count. It is satisfied by a poller.Poller[int],
// which refreshes the count in the background so the read is cheap.
type Counter interface {
	Current() int
}

// Handler exposes widget endpoints. It holds the auth verifier (nil when auth is
// disabled) and the required role so Routes can build its own write guard.
type Handler struct {
	core       *widgetcore.Core
	importer   *widgetimport.Importer
	counter    Counter
	translator *translation.Translator
	verifier   auth.Verifier
	role       string
}

// New builds a Handler. verifier is nil when auth is disabled, in which case the
// guard Routes builds is a no-op and every route stays public.
func New(core *widgetcore.Core, importer *widgetimport.Importer, counter Counter, translator *translation.Translator, verifier auth.Verifier, role string) *Handler {
	return &Handler{core: core, importer: importer, counter: counter, translator: translator, verifier: verifier, role: role}
}

// Routes registers the widget endpoints through the handle seam, so this
// transport does not depend on the router type — the server's Install function
// passes router.HandleApp. The signature is uniform across features (just the
// seam): authorization is the feature's own concern here — it builds a write
// guard from the injected verifier (auth.Guard) and attaches it to the writes.
// Reads stay public; the guard is a no-op when auth is disabled.
func (h *Handler) Routes(handle rest.Handle) {
	guard := auth.Guard(h.verifier, h.role) // nil (public) when auth is disabled

	handle("GET /widgets", h.query)
	handle("GET /widgets/count", h.count)
	// Keyset (cursor) pagination — the insert-stable, depth-cheap alternative to
	// ?page/?rows offset paging. The literal /cursor segment is matched ahead of
	// /{id} by net/http's pattern precedence.
	handle("GET /widgets/cursor", h.queryCursor)
	// JSON:API variants (same data, application/vnd.api+json via to.JSONAPI).
	handle("GET /widgets/jsonapi", h.queryJSONAPI)
	handle("GET /widgets/jsonapi/{id}", h.queryByIDJSONAPI)
	handle("GET /widgets/{id}", h.queryByID)

	handle("POST /widgets", h.create, guard)
	handle("POST /widgets/import", h.importBatch, guard)
	handle("PUT /widgets/{id}", h.update, guard)
	handle("DELETE /widgets/{id}", h.delete, guard)
	handle("POST /widgets/{id}/translations", h.saveTranslation, guard)
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
func (h *Handler) importBatch(ctx context.Context, r *http.Request) rest.ResponseEncoder {
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
func (h *Handler) create(ctx context.Context, r *http.Request) rest.ResponseEncoder {
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
func (h *Handler) query(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	q := r.URL.Query()
	pg, err := page.Parse(q.Get("page"), q.Get("rows"))
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	by, err := order.Parse(widgetcore.SortableFields, q.Get("order_by"), widgetcore.DefaultOrder)
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	filter := parseWidgetFilter(q)

	ws, err := h.core.Query(ctx, filter, by, pg)
	if err != nil {
		return errs.From(err)
	}
	total, err := h.core.Count(ctx, filter)
	if err != nil {
		return errs.From(err)
	}
	// PagedResponse wraps query.Result and implements translation.TranslatableList,
	// so the translationrest middleware still reaches and translates each item
	// before the result (items + total + page) is encoded.
	return PagedResponse{Result: query.NewResult(toResponseList(ws), total, pg)}
}

// queryCursor lists widgets with keyset (cursor) pagination: ?cursor=<token> (the
// opaque token from a prior page's next, empty for the first page), ?limit=N, and
// the same ?name/?description filter. It returns a CursorPagedResponse (the
// translatable counterpart of PagedResponse), so the translationrest middleware
// still localizes each widget. Forward-only — prev is not emitted.
func (h *Handler) queryCursor(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit")) // 0/invalid -> NewCursor applies the default
	cur := page.NewCursor(q.Get("cursor"), limit)
	if _, err := cur.Key(); err != nil {
		return errs.New(errs.InvalidArgument, err) // malformed cursor token -> 400
	}
	filter := parseWidgetFilter(q)

	ws, next, err := h.core.QueryByCursor(ctx, filter, cur)
	if err != nil {
		return errs.From(err)
	}
	return CursorPagedResponse{CursorResult: query.NewCursorResult(toResponseList(ws), next, "")}
}

// parseWidgetFilter reads the optional listing filter from the query string:
// ?name and ?description, each a case-insensitive substring. Absent params leave
// that field nil (not applied).
func parseWidgetFilter(q url.Values) widgetcore.QueryFilter {
	var f widgetcore.QueryFilter
	if name := strings.TrimSpace(q.Get("name")); name != "" {
		f.Name = &name
	}
	if desc := strings.TrimSpace(q.Get("description")); desc != "" {
		f.Description = &desc
	}
	return f
}

// count returns the cached total widget count. The value is refreshed in the
// background by a poller, so this read is cheap and never hits the database.
//
//	@Summary	Cached widget count
//	@Tags		widgets
//	@Produce	json
//	@Success	200	{object}	CountResponse
//	@Router		/widgets/count [get]
func (h *Handler) count(_ context.Context, _ *http.Request) rest.ResponseEncoder {
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
func (h *Handler) queryByID(ctx context.Context, r *http.Request) rest.ResponseEncoder {
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
func (h *Handler) queryJSONAPI(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	q := r.URL.Query()
	pg, err := page.Parse(q.Get("page"), q.Get("rows"))
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	by, err := order.Parse(widgetcore.SortableFields, q.Get("order_by"), widgetcore.DefaultOrder)
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}

	ws, err := h.core.Query(ctx, parseWidgetFilter(q), by, pg)
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
func (h *Handler) queryByIDJSONAPI(ctx context.Context, r *http.Request) rest.ResponseEncoder {
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
func (h *Handler) update(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", r.PathValue("id"))
	}

	var req UpdateWidgetReq
	if err = rest.Decode(r, &req); err != nil {
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
func (h *Handler) delete(ctx context.Context, r *http.Request) rest.ResponseEncoder {
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
func (h *Handler) saveTranslation(ctx context.Context, r *http.Request) rest.ResponseEncoder {
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
