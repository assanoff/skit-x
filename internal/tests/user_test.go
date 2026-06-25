package tests

import (
	"context"
	"net/http"
	"testing"

	"github.com/matryer/is"
)

// TestUserCRUD exercises the user module end to end (auth disabled by default in
// newTestServer, so writes are open). It mirrors the widget CRUD test against the
// /users endpoints and the query.Result list envelope.
func TestUserCRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	// Create.
	resp := doReq(t, srv, http.MethodPost, "/users", `{"email":"alice@example.com","name":"Alice"}`)
	is.Equal(resp.StatusCode, http.StatusCreated) // create -> 201
	var created map[string]any
	decode(t, resp, &created)
	id, _ := created["id"].(string)
	is.True(id != "")                               // id assigned
	is.Equal(created["email"], "alice@example.com") // echoes email

	// Get by id.
	resp = doReq(t, srv, http.MethodGet, "/users/"+id, "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var got map[string]any
	decode(t, resp, &got)
	is.Equal(got["name"], "Alice")

	// List: query.Result envelope {items,total,page,rowsPerPage}.
	resp = doReq(t, srv, http.MethodGet, "/users", "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var list struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	decode(t, resp, &list)
	is.Equal(len(list.Items), 1) // one user listed
	is.Equal(list.Total, 1)

	// Update (partial: name only) preserves email.
	resp = doReq(t, srv, http.MethodPut, "/users/"+id, `{"name":"Alicia"}`)
	is.Equal(resp.StatusCode, http.StatusOK)
	var updated map[string]any
	decode(t, resp, &updated)
	is.Equal(updated["name"], "Alicia")             // update applied
	is.Equal(updated["email"], "alice@example.com") // email preserved

	// Validation: a malformed email -> 400 invalid_argument.
	resp = doReq(t, srv, http.MethodPost, "/users", `{"email":"not-an-email","name":"x"}`)
	is.Equal(resp.StatusCode, http.StatusBadRequest)
	var errBody map[string]any
	decode(t, resp, &errBody)
	is.Equal(errBody["code"], "invalid_argument")

	// Duplicate email -> 409 already_exists (unique index).
	resp = doReq(t, srv, http.MethodPost, "/users", `{"email":"alice@example.com","name":"Clone"}`)
	is.Equal(resp.StatusCode, http.StatusConflict)
	_ = resp.Body.Close()

	// Delete -> 204, then 404.
	resp = doReq(t, srv, http.MethodDelete, "/users/"+id, "")
	is.Equal(resp.StatusCode, http.StatusNoContent)
	_ = resp.Body.Close()

	resp = doReq(t, srv, http.MethodGet, "/users/"+id, "")
	is.Equal(resp.StatusCode, http.StatusNotFound)
	_ = resp.Body.Close()
}
