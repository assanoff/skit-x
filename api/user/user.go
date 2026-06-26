// Package user is the REST transport for the user module. It demonstrates the
// per-route authorization pattern of the servicekit web layer: Routes builds a
// write guard from the injected verifier (auth.Guard) and attaches it to the
// write routes via the Handle seam, leaving reads public. The guard is a no-op
// when auth is disabled, so writes are then open. Contrast the product module,
// which guards its whole group at once in Install via router.WithApp. Handlers
// are rest.HandlerFunc values: they decode/validate input, call the Core, and
// return an Encoder (a DTO or an *errs.Error).
package user

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/assanoff/servicekit/auth"
	"github.com/assanoff/servicekit/errs"
	"github.com/assanoff/servicekit/order"
	"github.com/assanoff/servicekit/page"
	"github.com/assanoff/servicekit/query"
	"github.com/assanoff/servicekit/web/rest"
	"github.com/assanoff/servicekit/web/restmid"

	usercore "github.com/assanoff/service-kit-x/core/user"
)

// Handler exposes user endpoints. It holds the auth verifier (nil when auth is
// disabled) and the required role so Routes can build its own write guard.
type Handler struct {
	core     *usercore.Core
	verifier auth.Verifier
	role     string
}

// New builds a Handler. verifier is nil when auth is disabled, in which case the
// guard Routes builds is a no-op and every route stays public.
func New(core *usercore.Core, verifier auth.Verifier, role string) *Handler {
	return &Handler{core: core, verifier: verifier, role: role}
}

// Routes registers the user endpoints through the handle seam, so this transport
// does not depend on the router type — the server's Install passes router.HandleApp.
// The signature is uniform across features (just the seam): authorization is the
// feature's own concern here — it builds a write guard from the injected verifier
// and attaches it per route. Reads stay public; writes carry the guard (a no-op
// when auth is disabled). This is the per-route level; product instead guards its
// whole group in Install via WithApp.
func (h *Handler) Routes(handle rest.Handle) {
	guard := auth.Guard(h.verifier, h.role) // nil (public) when auth is disabled

	handle("GET /users", h.query)
	// Keyset (cursor) pagination — the insert-stable, depth-cheap alternative to
	// ?page/?rows offset paging. The literal /cursor segment is matched ahead of
	// /{id} by net/http's pattern precedence.
	handle("GET /users/cursor", h.queryCursor)
	// A cacheable read: per-handler app middleware (the developer's choice) —
	// Cache-Control + a conditional-GET ETag — without touching the other routes.
	handle("GET /users/{id}", h.queryByID, restmid.CacheControl(60), restmid.ETag())

	handle("POST /users", h.create, guard)
	handle("PUT /users/{id}", h.update, guard)
	handle("DELETE /users/{id}", h.delete, guard)
}

// create creates a single user.
func (h *Handler) create(ctx context.Context, r *http.Request) rest.Encoder {
	var req CreateUserReq
	if err := rest.Decode(r, &req); err != nil {
		return errs.From(err)
	}

	u, err := h.core.Create(ctx, usercore.NewUser{Email: req.Email, Name: req.Name})
	if err != nil {
		return errs.From(err)
	}
	return rest.JSONStatus(toResponse(u), http.StatusCreated)
}

// query lists one page of users (?page, ?rows), optionally narrowed by the filter
// params (?name, ?email) and ordered by ?order_by (e.g. "name,DESC"; default
// created_at DESC). Count uses the same filter so the total matches the set.
func (h *Handler) query(ctx context.Context, r *http.Request) rest.Encoder {
	q := r.URL.Query()
	pg, err := page.Parse(q.Get("page"), q.Get("rows"))
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	by, err := order.Parse(usercore.SortableFields, q.Get("order_by"), usercore.DefaultOrder)
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	filter := parseUserFilter(q)

	us, err := h.core.Query(ctx, filter, by, pg)
	if err != nil {
		return errs.From(err)
	}
	total, err := h.core.Count(ctx, filter)
	if err != nil {
		return errs.From(err)
	}
	return query.NewResult(toResponseList(us), total, pg)
}

// queryCursor lists users with keyset (cursor) pagination: ?cursor=<token> (the
// opaque token from a prior page's next, empty for the first page), ?limit=N, and
// the same ?name/?email filter. It returns a query.CursorResult with the items
// and the next-page token. Forward-only — prev is not emitted.
func (h *Handler) queryCursor(ctx context.Context, r *http.Request) rest.Encoder {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit")) // 0/invalid -> NewCursor applies the default
	cur := page.NewCursor(q.Get("cursor"), limit)
	if _, err := cur.Key(); err != nil {
		return errs.New(errs.InvalidArgument, err) // malformed cursor token -> 400
	}
	filter := parseUserFilter(q)

	us, next, err := h.core.QueryByCursor(ctx, filter, cur)
	if err != nil {
		return errs.From(err)
	}
	return query.NewCursorResult(toResponseList(us), next, "")
}

// parseUserFilter reads the optional listing filter from the query string: ?name
// and ?email, each a case-insensitive substring. Absent params leave that field
// nil (not applied).
func parseUserFilter(q url.Values) usercore.QueryFilter {
	var f usercore.QueryFilter
	if name := strings.TrimSpace(q.Get("name")); name != "" {
		f.Name = &name
	}
	if email := strings.TrimSpace(q.Get("email")); email != "" {
		f.Email = &email
	}
	return f
}

// queryByID returns one user by id.
func (h *Handler) queryByID(ctx context.Context, r *http.Request) rest.Encoder {
	id, err := uuid.Parse(rest.Param(r, "id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", rest.Param(r, "id"))
	}
	u, err := h.core.QueryByID(ctx, id)
	if err != nil {
		return errs.From(err)
	}
	return toResponse(u)
}

// update applies a partial update to a user.
func (h *Handler) update(ctx context.Context, r *http.Request) rest.Encoder {
	id, err := uuid.Parse(rest.Param(r, "id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", rest.Param(r, "id"))
	}

	var req UpdateUserReq
	if err := rest.Decode(r, &req); err != nil {
		return errs.From(err)
	}

	u, err := h.core.Update(ctx, id, usercore.UpdateUser{Email: req.Email, Name: req.Name})
	if err != nil {
		return errs.From(err)
	}
	return rest.JSON(toResponse(u))
}

// delete removes a user.
func (h *Handler) delete(ctx context.Context, r *http.Request) rest.Encoder {
	id, err := uuid.Parse(rest.Param(r, "id"))
	if err != nil {
		return errs.Newf(errs.InvalidArgument, "invalid id %q", rest.Param(r, "id"))
	}
	if err := h.core.Delete(ctx, id); err != nil {
		return errs.From(err)
	}
	return nil // 204 No Content
}
