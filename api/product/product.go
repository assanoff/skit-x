// Package product is the REST transport for the product module. It demonstrates
// the group-scoped authorization pattern of the skit web layer: it
// registers no per-route auth, because the whole product group is guarded by the
// server's Install via router.WithApp(authMW...). Every product endpoint —
// including reads — therefore requires authorization when auth is enabled.
// Contrast the user module, which guards only its writes per route. Handlers are
// rest.HandlerFunc values returning a ResponseEncoder (a DTO or an *errs.Error).
package product

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"uuid"

	"github.com/assanoff/skit/errs"
	"github.com/assanoff/skit/order"
	"github.com/assanoff/skit/page"
	"github.com/assanoff/skit/query"
	"github.com/assanoff/skit/rest"

	productcore "github.com/assanoff/skit-x/core/product"
)

// Handler exposes product endpoints.
type Handler struct {
	core *productcore.Core
}

// New builds a Handler.
func New(core *productcore.Core) *Handler {
	return &Handler{core: core}
}

// Routes registers the product endpoints through the handle seam. It passes no
// per-route middleware: the server's Install hands it the HandleApp of a group
// already wrapped with auth (api.WithApp(authMW...)), so authorization applies to
// the whole group at once — the group-scoped counterpart of user's per-route auth.
func (h *Handler) Routes(handle rest.Handle) {
	handle("GET /products", h.query)
	// Keyset (cursor) pagination — the alternative to ?page/?rows offset paging:
	// stable under concurrent inserts and cheap at any depth. The literal /cursor
	// segment is matched ahead of /{id} by net/http's pattern precedence.
	handle("GET /products/cursor", h.queryCursor)
	handle("GET /products/{id}", h.queryByID)
	handle("POST /products", h.create)
	handle("PUT /products/{id}", h.update)
	handle("DELETE /products/{id}", h.delete)
}

// create creates a single product.
func (h *Handler) create(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	var req CreateProductReq
	if err := rest.Decode(r, &req); err != nil {
		return errs.From(err)
	}

	p, err := h.core.Create(ctx, productcore.NewProduct{Name: req.Name, Price: req.Price})
	if err != nil {
		return errs.From(err)
	}
	return rest.JSONStatus(toResponse(p), http.StatusCreated)
}

// query lists one page of products (?page, ?rows), optionally narrowed by the
// filter params (?name, ?min_price, ?max_price) and ordered by ?order_by
// (e.g. "name,DESC"; default created_at DESC). Count uses the same filter so the
// total matches the filtered set.
func (h *Handler) query(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	q := r.URL.Query()
	pg, err := page.Parse(q.Get("page"), q.Get("rows"))
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	by, err := order.Parse(productcore.SortableFields, q.Get("order_by"), productcore.DefaultOrder)
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	filter, err := parseProductFilter(q)
	if err != nil {
		return errs.From(err)
	}

	ps, err := h.core.Query(ctx, filter, by, pg)
	if err != nil {
		return errs.From(err)
	}
	total, err := h.core.Count(ctx, filter)
	if err != nil {
		return errs.From(err)
	}
	return query.NewResult(toResponseList(ps), total, pg)
}

// queryCursor lists products with keyset (cursor) pagination: ?cursor=<token>
// (the opaque token from a prior page's next, empty for the first page) and
// ?limit=N. It returns a query.CursorResult carrying the items and the next-page
// token. Forward-only — prev is not emitted.
func (h *Handler) queryCursor(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit")) // 0/invalid -> NewCursor applies the default
	cur := page.NewCursor(q.Get("cursor"), limit)
	if _, err := cur.Key(); err != nil {
		return errs.New(errs.InvalidArgument, err) // malformed cursor token -> 400
	}
	filter, err := parseProductFilter(q)
	if err != nil {
		return errs.From(err)
	}

	ps, next, err := h.core.QueryByCursor(ctx, filter, cur)
	if err != nil {
		return errs.From(err)
	}
	return query.NewCursorResult(toResponseList(ps), next, "")
}

// parseProductFilter reads the optional listing filter from the query string:
// ?name (case-insensitive substring), ?min_price / ?max_price (inclusive bounds).
// A non-numeric price bound is a 400; absent params leave that field nil.
func parseProductFilter(q url.Values) (productcore.QueryFilter, error) {
	var f productcore.QueryFilter

	if name := strings.TrimSpace(q.Get("name")); name != "" {
		f.Name = &name
	}

	if err := parseOptInt(q, "min_price", &f.MinPrice); err != nil {
		return productcore.QueryFilter{}, err
	}
	if err := parseOptInt(q, "max_price", &f.MaxPrice); err != nil {
		return productcore.QueryFilter{}, err
	}
	return f, nil
}

// parseOptInt parses the optional int64 query param key into dst: an absent
// param leaves dst nil (not applied), a non-numeric one is an InvalidArgument.
func parseOptInt(q url.Values, key string, dst **int64) error {
	s := q.Get(key)
	if s == "" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid %s: %v", key, err)
	}
	*dst = &n
	return nil
}

// queryByID returns one product by id.
func (h *Handler) queryByID(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	id, err := uuid.Parse(rest.Param(r, "id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", rest.Param(r, "id"))
	}
	p, err := h.core.QueryByID(ctx, id)
	if err != nil {
		return errs.From(err)
	}
	return toResponse(p)
}

// update applies a partial update to a product.
func (h *Handler) update(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	id, err := uuid.Parse(rest.Param(r, "id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", rest.Param(r, "id"))
	}

	var req UpdateProductReq
	if err = rest.Decode(r, &req); err != nil {
		return errs.From(err)
	}

	p, err := h.core.Update(ctx, id, productcore.UpdateProduct{Name: req.Name, Price: req.Price})
	if err != nil {
		return errs.From(err)
	}
	return rest.JSON(toResponse(p))
}

// delete removes a product.
func (h *Handler) delete(ctx context.Context, r *http.Request) rest.ResponseEncoder {
	id, err := uuid.Parse(rest.Param(r, "id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", rest.Param(r, "id"))
	}
	if err := h.core.Delete(ctx, id); err != nil {
		return errs.From(err)
	}
	return nil // 204 No Content
}
