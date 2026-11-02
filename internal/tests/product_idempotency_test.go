package tests

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/matryer/is"

	"github.com/assanoff/skit-x/internal/app/idempotency"
)

// TestProductCreateIdempotent retries POST /products with one Idempotency-Key:
// the retry gets the same status and body, and one product exists.
func TestProductCreateIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)
	srv, _ := newTestServer(context.Background(), t)

	post := func(key string) (int, []byte) {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/products",
			bytes.NewBufferString(`{"name":"Mouse","price":1999}`))
		is.NoErr(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(idempotency.Header, key)
		resp, err := srv.Client().Do(req)
		is.NoErr(err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		is.NoErr(err)
		return resp.StatusCode, body
	}

	status, first := post("order-1")
	is.Equal(status, http.StatusCreated)
	status, retry := post("order-1")
	is.Equal(status, http.StatusCreated)   // the retry gets the stored status
	is.Equal(string(retry), string(first)) // and body: the same product id
	status, other := post("order-2")
	is.Equal(status, http.StatusCreated)
	is.True(string(other) != string(first)) // another key creates another product

	var list struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/products", ""), &list)
	is.Equal(len(list.Data.Items), 2) // two keys, two products
}
