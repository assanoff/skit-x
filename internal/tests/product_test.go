package tests

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"

	"github.com/assanoff/skit/logger"

	"github.com/assanoff/skit-x/internal/app/server"
)

// TestProductCRUD exercises the product module end to end (auth disabled by
// default, so the group is open).
func TestProductCRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	// Create.
	resp := doReq(t, srv, http.MethodPost, "/products", `{"name":"Keyboard","price":4999}`)
	is.Equal(resp.StatusCode, http.StatusCreated)
	var created map[string]any
	decode(t, resp, &created)
	id, _ := created["id"].(string)
	is.True(id != "")                         // id assigned
	is.Equal(created["price"], float64(4999)) // price echoed (JSON number)

	// Get by id.
	resp = doReq(t, srv, http.MethodGet, "/products/"+id, "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var got map[string]any
	decode(t, resp, &got)
	is.Equal(got["name"], "Keyboard")

	// Update (partial: price only) preserves name.
	resp = doReq(t, srv, http.MethodPut, "/products/"+id, `{"price":3999}`)
	is.Equal(resp.StatusCode, http.StatusOK)
	var updated map[string]any
	decode(t, resp, &updated)
	is.Equal(updated["price"], float64(3999)) // update applied
	is.Equal(updated["name"], "Keyboard")     // name preserved

	// Delete -> 204, then 404.
	resp = doReq(t, srv, http.MethodDelete, "/products/"+id, "")
	is.Equal(resp.StatusCode, http.StatusNoContent)
	_ = resp.Body.Close()

	resp = doReq(t, srv, http.MethodGet, "/products/"+id, "")
	is.Equal(resp.StatusCode, http.StatusNotFound)
	_ = resp.Body.Close()
}

// TestGroupVsRouteAuth contrasts the two authorization patterns under one server
// with auth enabled: product is guarded as a whole group (api.WithApp), so even
// reads need a token; user guards only writes (per-route), so user reads stay
// public.
func TestGroupVsRouteAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	cfg := startPostgres(ctx, t)
	cfg.Auth.Enabled = true
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Auth.RequiredRole = "admin"
	cfg.HTTP.RequestTimeout = 10 * time.Second

	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})
	slog.SetDefault(log.Slog())
	handler, err := server.Handler(ctx, cfg, log)
	is.NoErr(err) // build handler
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// product group is fully guarded (WithApp): a read without a token -> 401.
	resp := doReq(t, srv, http.MethodGet, "/products", "")
	is.Equal(resp.StatusCode, http.StatusUnauthorized) // product reads guarded
	_ = resp.Body.Close()

	// user reads stay public even with auth enabled (per-route auth on writes only).
	resp = doReq(t, srv, http.MethodGet, "/users", "")
	is.Equal(resp.StatusCode, http.StatusOK) // user reads public
	_ = resp.Body.Close()

	// user writes are guarded -> 401 without a token.
	resp = doReq(t, srv, http.MethodPost, "/users", `{"email":"bob@example.com","name":"Bob"}`)
	is.Equal(resp.StatusCode, http.StatusUnauthorized) // user writes guarded
	_ = resp.Body.Close()

	// With a valid admin token, the product read succeeds.
	token := signToken(t, "test-secret", "admin")
	resp = getWithToken(t, srv, "/products", token)
	is.Equal(resp.StatusCode, http.StatusOK) // authorized product read
	_ = resp.Body.Close()
}

// TestProductCursorPagination walks the keyset (cursor) endpoint end to end:
// it creates N products, pages through GET /products/cursor following the next
// token, and asserts every product is visited exactly once and the cursor
// terminates (empty next) on the last page.
func TestProductCursorPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	const total = 5
	created := map[string]bool{}
	for i := range total {
		body := fmt.Sprintf(`{"name":"P%d","price":%d}`, i, (i+1)*100)
		resp := doReq(t, srv, http.MethodPost, "/products", body)
		is.Equal(resp.StatusCode, http.StatusCreated)
		var c map[string]any
		decode(t, resp, &c)
		created[c["id"].(string)] = true
	}

	// Page with limit=2, following next until it is empty: 2 + 2 + 1 = 3 pages.
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/products/cursor?limit=2"
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

	is.Equal(len(seen), total) // every product visited exactly once
	for id := range created {
		is.True(seen[id]) // and they are the ones we created
	}
	is.Equal(pages, 3) // 2 + 2 + 1
}

// TestProductListPaginationMeta verifies the offset list envelope carries the
// derived pagination metadata (total_items, current_page, total_pages, limit).
func TestProductListPaginationMeta(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	for i := range 5 {
		resp := doReq(t, srv, http.MethodPost, "/products", fmt.Sprintf(`{"name":"P%d","price":100}`, i))
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

	// Page 1 of rows=2 over 5 rows: 3 pages, current page 1.
	resp := doReq(t, srv, http.MethodGet, "/products?page=1&rows=2", "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var p1 meta
	decode(t, resp, &p1)
	is.Equal(p1.Data.Pagination.TotalItems, 5)  // total count
	is.Equal(p1.Data.Pagination.TotalPages, 3)  // ceil(5/2)
	is.Equal(p1.Data.Pagination.CurrentPage, 1) // first page
	is.Equal(p1.Data.Pagination.Limit, 2)       // page size
	is.Equal(len(p1.Data.Items), 2)             // full page

	// Last page: current page 3, one remaining row.
	resp = doReq(t, srv, http.MethodGet, "/products?page=3&rows=2", "")
	is.Equal(resp.StatusCode, http.StatusOK)
	var p3 meta
	decode(t, resp, &p3)
	is.Equal(p3.Data.Pagination.CurrentPage, 3) // last page
	is.Equal(p3.Data.Pagination.TotalPages, 3)  // still three pages
	is.Equal(len(p3.Data.Items), 1)             // remainder
}

// TestProductFilter exercises the QueryFilter path (?name, ?min_price,
// ?max_price): the name substring is case-insensitive, the price bounds are
// inclusive, filters combine, the envelope total reflects the filter (Count uses
// it too), and a non-numeric bound is a 400.
func TestProductFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	seed := []struct {
		name  string
		price int
	}{
		{"Apple Keyboard", 5000},
		{"Apple Mouse", 3000},
		{"Dell Monitor", 20000},
		{"Logitech Mouse", 2500},
	}
	for _, p := range seed {
		resp := doReq(t, srv, http.MethodPost, "/products", fmt.Sprintf(`{"name":%q,"price":%d}`, p.name, p.price))
		is.Equal(resp.StatusCode, http.StatusCreated)
		_ = resp.Body.Close()
	}

	type listResp struct {
		Data struct {
			Items []struct {
				Name  string `json:"name"`
				Price int64  `json:"price"`
			} `json:"items"`
			Pagination struct {
				TotalItems int `json:"total_items"`
			} `json:"pagination"`
		} `json:"data"`
	}
	list := func(qs string) listResp {
		resp := doReq(t, srv, http.MethodGet, "/products?rows=100&"+qs, "")
		is.Equal(resp.StatusCode, http.StatusOK)
		var lr listResp
		decode(t, resp, &lr)
		return lr
	}

	// name: case-insensitive substring.
	byName := list("name=mouse")
	is.Equal(byName.Data.Pagination.TotalItems, 2) // Count honors the filter
	is.Equal(len(byName.Data.Items), 2)            // both mice
	for _, it := range byName.Data.Items {
		is.True(strings.Contains(strings.ToLower(it.Name), "mouse"))
	}

	// price: inclusive range [3000, 6000].
	byPrice := list("min_price=3000&max_price=6000")
	is.Equal(byPrice.Data.Pagination.TotalItems, 2) // Apple Keyboard (5000) + Apple Mouse (3000)
	for _, it := range byPrice.Data.Items {
		is.True(it.Price >= 3000 && it.Price <= 6000)
	}

	// combined name + max_price.
	combined := list("name=apple&max_price=4000")
	is.Equal(combined.Data.Pagination.TotalItems, 1) // only Apple Mouse (3000)
	is.Equal(combined.Data.Items[0].Name, "Apple Mouse")

	// non-numeric bound -> 400.
	resp := doReq(t, srv, http.MethodGet, "/products?min_price=abc", "")
	is.Equal(resp.StatusCode, http.StatusBadRequest)
	_ = resp.Body.Close()
}

// TestProductOrdering exercises ?order_by against the allowlist: name/price in
// both directions, the default (created_at DESC, newest first), and 400s for an
// unknown field or direction.
func TestProductOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	// Created in this order, so newest-first (the default) is Cherry, Apple, Banana.
	seed := []struct {
		name  string
		price int
	}{
		{"Banana", 300},
		{"Apple", 100},
		{"Cherry", 200},
	}
	for _, p := range seed {
		resp := doReq(t, srv, http.MethodPost, "/products", fmt.Sprintf(`{"name":%q,"price":%d}`, p.name, p.price))
		is.Equal(resp.StatusCode, http.StatusCreated)
		_ = resp.Body.Close()
	}

	names := func(qs string) []string {
		resp := doReq(t, srv, http.MethodGet, "/products?rows=100&"+qs, "")
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
	is.Equal(names("order_by=price,ASC"), []string{"Apple", "Cherry", "Banana"}) // price 100,200,300
	is.Equal(names(""), []string{"Cherry", "Apple", "Banana"})                   // default: created_at DESC

	// Unknown field / direction -> 400.
	resp := doReq(t, srv, http.MethodGet, "/products?order_by=bogus", "")
	is.Equal(resp.StatusCode, http.StatusBadRequest)
	_ = resp.Body.Close()

	resp = doReq(t, srv, http.MethodGet, "/products?order_by=name,sideways", "")
	is.Equal(resp.StatusCode, http.StatusBadRequest)
	_ = resp.Body.Close()
}

// getWithToken issues a GET carrying a bearer token.
func getWithToken(t *testing.T, srv *httptest.Server, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}
