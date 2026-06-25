package tests

import (
	"context"
	"net/http"
	"testing"

	"github.com/matryer/is"
)

// TestUserCaching exercises the per-handler CacheControl + ETag middleware on
// GET /users/{id}: the first read carries Cache-Control and an ETag, and a
// conditional read with a matching If-None-Match returns 304 Not Modified.
func TestUserCaching(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	resp := doReq(t, srv, http.MethodPost, "/users", `{"email":"cache@example.com","name":"Cache"}`)
	is.Equal(resp.StatusCode, http.StatusCreated)
	var created map[string]any
	decode(t, resp, &created)
	id, _ := created["id"].(string)
	is.True(id != "")

	// First read: 200 with Cache-Control + ETag set by the per-route middleware.
	resp = doReq(t, srv, http.MethodGet, "/users/"+id, "")
	is.Equal(resp.StatusCode, http.StatusOK)
	is.Equal(resp.Header.Get("Cache-Control"), "public, max-age=60")
	etag := resp.Header.Get("ETag")
	is.True(etag != "") // ETag present
	_ = resp.Body.Close()

	// Conditional read: a matching If-None-Match yields 304 Not Modified.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/users/"+id, nil)
	is.NoErr(err)
	req.Header.Set("If-None-Match", etag)
	resp, err = srv.Client().Do(req)
	is.NoErr(err)
	is.Equal(resp.StatusCode, http.StatusNotModified) // conditional GET -> 304
	_ = resp.Body.Close()
}
