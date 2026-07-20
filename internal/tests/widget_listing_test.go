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

// TestWidgetCursorPagination walks the keyset (cursor) endpoint end to end: it
// creates N widgets, pages through GET /widgets/cursor following the next token,
// and asserts every widget is visited exactly once and the cursor terminates
// (empty next) on the last page.
func TestWidgetCursorPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	const total = 5
	created := map[string]bool{}
	for i := range total {
		body := fmt.Sprintf(`{"name":"W%d","description":"d%d"}`, i, i)
		resp := doReq(t, srv, http.MethodPost, "/widgets", body)
		is.Equal(resp.StatusCode, http.StatusCreated)
		var c map[string]any
		decode(t, resp, &c)
		created[c["id"].(string)] = true
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/widgets/cursor?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		resp := doReq(t, srv, http.MethodGet, path, "")
		is.Equal(resp.StatusCode, http.StatusOK)

		var pr struct {
			Data struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
				Next string `json:"next"`
				Prev string `json:"prev"`
			} `json:"data"`
		}
		decode(t, resp, &pr)

		is.Equal(pr.Data.Prev, "") // forward-only: prev is never emitted
		for _, it := range pr.Data.Items {
			is.True(!seen[it.ID]) // no row repeats across pages
			seen[it.ID] = true
		}
		pages++
		if pr.Data.Next == "" {
			break
		}
		is.Equal(len(pr.Data.Items), 2) // a full page precedes a next cursor
		cursor = pr.Data.Next
		if pages > total {
			t.Fatal("cursor did not terminate")
		}
	}

	is.Equal(len(seen), total) // every widget visited exactly once
	for id := range created {
		is.True(seen[id]) // and they are the ones we created
	}
	is.Equal(pages, 3) // 2 + 2 + 1
}

// TestWidgetListPaginationMeta verifies the offset list envelope carries the
// derived pagination metadata (total_items, current_page, total_pages, limit).
func TestWidgetListPaginationMeta(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	for i := range 5 {
		resp := doReq(t, srv, http.MethodPost, "/widgets", fmt.Sprintf(`{"name":"W%d","description":""}`, i))
		is.Equal(resp.StatusCode, http.StatusCreated)
		_ = resp.Body.Close()
	}

	type meta struct {
		Data struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			Pagination struct {
				TotalItems  int `json:"total_items"`
				CurrentPage int `json:"current_page"`
				TotalPages  int `json:"total_pages"`
				Limit       int `json:"limit"`
			} `json:"pagination"`
		} `json:"data"`
	}

	resp := doReq(t, srv, http.MethodGet, "/widgets?page=1&rows=2", "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var p1 meta
	decode(t, resp, &p1)
	is.Equal(p1.Data.Pagination.TotalItems, 5)  // total count
	is.Equal(p1.Data.Pagination.TotalPages, 3)  // ceil(5/2)
	is.Equal(p1.Data.Pagination.CurrentPage, 1) // first page
	is.Equal(p1.Data.Pagination.Limit, 2)       // page size
	is.Equal(len(p1.Data.Items), 2)             // full page

	resp = doReq(t, srv, http.MethodGet, "/widgets?page=3&rows=2", "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var p3 meta
	decode(t, resp, &p3)
	is.Equal(p3.Data.Pagination.CurrentPage, 3) // last page
	is.Equal(p3.Data.Pagination.TotalPages, 3)  // still three pages
	is.Equal(len(p3.Data.Items), 1)             // remainder
}

// TestWidgetFilter exercises the QueryFilter path (?name, ?description): both are
// case-insensitive substrings, they combine, and the envelope total reflects the
// filter (Count uses it too).
func TestWidgetFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	seed := []struct{ name, desc string }{
		{"Alpha Gadget", "red"},
		{"Beta Gadget", "blue"},
		{"Gamma Widget", "red"},
	}
	for _, w := range seed {
		resp := doReq(t, srv, http.MethodPost, "/widgets", fmt.Sprintf(`{"name":%q,"description":%q}`, w.name, w.desc))
		is.Equal(resp.StatusCode, http.StatusCreated)
		_ = resp.Body.Close()
	}

	type listResp struct {
		Data struct {
			Items []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"items"`
			Pagination struct {
				TotalItems int `json:"total_items"`
			} `json:"pagination"`
		} `json:"data"`
	}
	list := func(qs string) listResp {
		resp := doReq(t, srv, http.MethodGet, "/widgets?rows=100&"+qs, "")
		is.Equal(resp.StatusCode, http.StatusOK)
		var lr listResp
		decode(t, resp, &lr)
		return lr
	}

	// name: case-insensitive substring -> the two "Gadget"s.
	byName := list("name=gadget")
	is.Equal(byName.Data.Pagination.TotalItems, 2)
	for _, it := range byName.Data.Items {
		is.True(strings.Contains(strings.ToLower(it.Name), "gadget"))
	}

	// description substring -> the two "red"s.
	byDesc := list("description=red")
	is.Equal(byDesc.Data.Pagination.TotalItems, 2)
	for _, it := range byDesc.Data.Items {
		is.Equal(it.Description, "red")
	}

	// combined name + description -> Alpha Gadget only.
	combined := list("name=gadget&description=red")
	is.Equal(combined.Data.Pagination.TotalItems, 1)
	is.Equal(combined.Data.Items[0].Name, "Alpha Gadget")
}

// TestWidgetOrdering exercises ?order_by against the allowlist (created_at, name):
// name in both directions, the default (created_at DESC, newest first), and 400s
// for an unknown field (description is not sortable) or direction.
func TestWidgetOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	// Created in this order, so newest-first (default) is Cherry, Apple, Banana.
	for _, name := range []string{"Banana", "Apple", "Cherry"} {
		resp := doReq(t, srv, http.MethodPost, "/widgets", fmt.Sprintf(`{"name":%q,"description":""}`, name))
		is.Equal(resp.StatusCode, http.StatusCreated)
		_ = resp.Body.Close()
	}

	names := func(qs string) []string {
		resp := doReq(t, srv, http.MethodGet, "/widgets?rows=100&"+qs, "")
		is.Equal(resp.StatusCode, http.StatusOK)
		var lr struct {
			Data struct {
				Items []struct {
					Name string `json:"name"`
				} `json:"items"`
			} `json:"data"`
		}
		decode(t, resp, &lr)
		out := make([]string, len(lr.Data.Items))
		for i, it := range lr.Data.Items {
			out[i] = it.Name
		}
		return out
	}

	is.Equal(names("order_by=name,ASC"), []string{"Apple", "Banana", "Cherry"})  // name ascending
	is.Equal(names("order_by=name,DESC"), []string{"Cherry", "Banana", "Apple"}) // name descending
	is.Equal(names(""), []string{"Cherry", "Apple", "Banana"})                   // default: created_at DESC

	// description is not in the sortable allowlist -> 400.
	resp := doReq(t, srv, http.MethodGet, "/widgets?order_by=description", "")
	is.Equal(resp.StatusCode, http.StatusBadRequest)
	_ = resp.Body.Close()

	resp = doReq(t, srv, http.MethodGet, "/widgets?order_by=name,sideways", "")
	is.Equal(resp.StatusCode, http.StatusBadRequest)
	_ = resp.Body.Close()
}
