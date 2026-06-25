// Package user is the REST transport for the user module. It demonstrates the
// per-route authorization pattern of the servicekit web layer: reads are public
// and each write passes authMW (rest.MidFunc), so writes are guarded when auth
// is enabled and open otherwise. Contrast the product module, which guards its
// whole group via router.WithApp. Handlers are rest.HandlerFunc values: they
// decode/validate input, call the Core, and return an Encoder (a DTO or an
// *errs.Error).
package user

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/assanoff/servicekit/errs"
	"github.com/assanoff/servicekit/page"
	"github.com/assanoff/servicekit/query"
	"github.com/assanoff/servicekit/web/rest"
	"github.com/assanoff/servicekit/web/restmid"

	usercore "github.com/assanoff/service-kit-x/core/user"
)

// Handler exposes user endpoints.
type Handler struct {
	core *usercore.Core
}

// New builds a Handler.
func New(core *usercore.Core) *Handler {
	return &Handler{core: core}
}

// Routes registers the user endpoints through the handle seam, so this transport
// does not depend on the router type — the server's Install passes router.HandleApp.
// Reads are public; each write passes authMW per route, so it requires
// authorization when auth is enabled and is unguarded otherwise.
func (h *Handler) Routes(handle rest.Handle, authMW ...rest.MidFunc) {
	handle("GET /users", h.query)
	// A cacheable read: per-handler app middleware (the developer's choice) adds
	// Cache-Control + a conditional-GET ETag without touching the other routes.
	handle("GET /users/{id}", h.queryByID, restmid.CacheControl(60), restmid.ETag())

	handle("POST /users", h.create, authMW...)
	handle("PUT /users/{id}", h.update, authMW...)
	handle("DELETE /users/{id}", h.delete, authMW...)
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

// query lists one page of users (?page, ?rows).
func (h *Handler) query(ctx context.Context, r *http.Request) rest.Encoder {
	pg, err := page.Parse(r.URL.Query().Get("page"), r.URL.Query().Get("rows"))
	if err != nil {
		return errs.New(errs.InvalidArgument, err)
	}
	us, err := h.core.Query(ctx, pg)
	if err != nil {
		return errs.From(err)
	}
	total, err := h.core.Count(ctx)
	if err != nil {
		return errs.From(err)
	}
	return query.NewResult(toResponseList(us), total, pg)
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
