package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

// TestUserCursorPagination walks the keyset (cursor) endpoint end to end: it
// creates N users, pages through GET /users/cursor following the next token, and
// asserts every user is visited exactly once and the cursor terminates (empty
// next) on the last page.
func TestUserCursorPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	const total = 5
	created := map[string]bool{}
	for i := 0; i < total; i++ {
		body := fmt.Sprintf(`{"email":"u%d@example.com","name":"U%d"}`, i, i)
		resp := doReq(t, srv, http.MethodPost, "/users", body)
		is.Equal(resp.StatusCode, http.StatusCreated)
		var c map[string]any
		decode(t, resp, &c)
		created[c["id"].(string)] = true
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/users/cursor?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		resp := doReq(t, srv, http.MethodGet, path, "")
		is.Equal(resp.StatusCode, http.StatusOK)

		var pr struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			Next string `json:"next"`
			Prev string `json:"prev"`
		}
		decode(t, resp, &pr)

		is.Equal(pr.Prev, "") // forward-only: prev is never emitted
		for _, it := range pr.Items {
			is.True(!seen[it.ID]) // no row repeats across pages
			seen[it.ID] = true
		}
		pages++
		if pr.Next == "" {
			break
		}
		is.Equal(len(pr.Items), 2) // a full page precedes a next cursor
		cursor = pr.Next
		if pages > total {
			t.Fatal("cursor did not terminate")
		}
	}

	is.Equal(len(seen), total) // every user visited exactly once
	for id := range created {
		is.True(seen[id]) // and they are the ones we created
	}
	is.Equal(pages, 3) // 2 + 2 + 1
}

// TestUserListPaginationMeta verifies the offset list envelope carries the
// derived pagination metadata (totalPages and the prev/next page numbers, the
// latter omitted at the edges).
func TestUserListPaginationMeta(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	for i := 0; i < 5; i++ {
		resp := doReq(t, srv, http.MethodPost, "/users", fmt.Sprintf(`{"email":"u%d@example.com","name":"U%d"}`, i, i))
		is.Equal(resp.StatusCode, http.StatusCreated)
		_ = resp.Body.Close()
	}

	type meta struct {
		Total      int `json:"total"`
		Page       int `json:"page"`
		TotalPages int `json:"totalPages"`
		Prev       int `json:"prev"`
		Next       int `json:"next"`
		Items      []struct {
			ID string `json:"id"`
		} `json:"items"`
	}

	resp := doReq(t, srv, http.MethodGet, "/users?page=1&rows=2", "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var p1 meta
	decode(t, resp, &p1)
	is.Equal(p1.Total, 5)      // total count
	is.Equal(p1.TotalPages, 3) // ceil(5/2)
	is.Equal(p1.Prev, 0)       // no previous page (omitted)
	is.Equal(p1.Next, 2)       // next page
	is.Equal(len(p1.Items), 2) // full page

	resp = doReq(t, srv, http.MethodGet, "/users?page=3&rows=2", "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var p3 meta
	decode(t, resp, &p3)
	is.Equal(p3.Page, 3)       // last page
	is.Equal(p3.Prev, 2)       // previous page
	is.Equal(p3.Next, 0)       // no next page (omitted)
	is.Equal(len(p3.Items), 1) // remainder
}

// TestUserFilter exercises the QueryFilter path (?name, ?email): both are
// case-insensitive substrings, they combine, and the envelope total reflects the
// filter (Count uses it too).
func TestUserFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	seed := []struct{ name, email string }{
		{"Alice", "alice@acme.com"},
		{"Bob", "bob@acme.com"},
		{"Carol", "carol@other.com"},
		{"Alfred", "alfred@acme.com"},
	}
	for _, u := range seed {
		resp := doReq(t, srv, http.MethodPost, "/users", fmt.Sprintf(`{"email":%q,"name":%q}`, u.email, u.name))
		is.Equal(resp.StatusCode, http.StatusCreated)
		_ = resp.Body.Close()
	}

	type listResp struct {
		Total int `json:"total"`
		Items []struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"items"`
	}
	list := func(qs string) listResp {
		resp := doReq(t, srv, http.MethodGet, "/users?rows=100&"+qs, "")
		is.Equal(resp.StatusCode, http.StatusOK)
		var lr listResp
		decode(t, resp, &lr)
		return lr
	}

	// name: case-insensitive substring "al" -> Alice, Alfred.
	byName := list("name=al")
	is.Equal(byName.Total, 2)
	for _, it := range byName.Items {
		is.True(strings.Contains(strings.ToLower(it.Name), "al"))
	}

	// email domain substring -> acme has Alice, Bob, Alfred.
	byEmail := list("email=acme")
	is.Equal(byEmail.Total, 3)
	for _, it := range byEmail.Items {
		is.True(strings.Contains(it.Email, "acme"))
	}

	// combined name + email.
	combined := list("name=al&email=acme")
	is.Equal(combined.Total, 2) // Alice, Alfred (both al* and @acme)
}

// TestUserOrdering exercises ?order_by against the allowlist: name/email in both
// directions, the default (created_at DESC, newest first), and 400s for an
// unknown field or direction.
func TestUserOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	// Created in this order, so newest-first (default) is Cherry, Apple, Banana.
	seed := []struct{ name, email string }{
		{"Banana", "banana@x.com"},
		{"Apple", "apple@x.com"},
		{"Cherry", "cherry@x.com"},
	}
	for _, u := range seed {
		resp := doReq(t, srv, http.MethodPost, "/users", fmt.Sprintf(`{"email":%q,"name":%q}`, u.email, u.name))
		is.Equal(resp.StatusCode, http.StatusCreated)
		_ = resp.Body.Close()
	}

	names := func(qs string) []string {
		resp := doReq(t, srv, http.MethodGet, "/users?rows=100&"+qs, "")
		is.Equal(resp.StatusCode, http.StatusOK)
		var lr struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
		}
		decode(t, resp, &lr)
		out := make([]string, len(lr.Items))
		for i, it := range lr.Items {
			out[i] = it.Name
		}
		return out
	}

	is.Equal(names("order_by=name,ASC"), []string{"Apple", "Banana", "Cherry"})  // name ascending
	is.Equal(names("order_by=name,DESC"), []string{"Cherry", "Banana", "Apple"}) // name descending
	is.Equal(names("order_by=email,ASC"), []string{"Apple", "Banana", "Cherry"}) // apple<banana<cherry
	is.Equal(names(""), []string{"Cherry", "Apple", "Banana"})                   // default: created_at DESC

	resp := doReq(t, srv, http.MethodGet, "/users?order_by=bogus", "")
	is.Equal(resp.StatusCode, http.StatusBadRequest)
	_ = resp.Body.Close()

	resp = doReq(t, srv, http.MethodGet, "/users?order_by=name,sideways", "")
	is.Equal(resp.StatusCode, http.StatusBadRequest)
	_ = resp.Body.Close()
}
