package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWidgetTranslation exercises the translation demo end to end: a widget is
// created in the default language (ru), a Kazakh translation is saved via
// POST /widgets/{id}/translations, and reads are auto-translated by the
// translationrest middleware when X-Language: kk is sent — while the default
// language still returns the canonical content.
func TestWidgetTranslation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	// Create the canonical (Russian) widget.
	created := mustDo(t, srv, http.MethodPost, "/widgets", `{"name":"gadget","description":"shiny"}`, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("expected an id, got %v", created)
	}

	// Save a Kazakh translation of its content.
	mustDo(t, srv, http.MethodPost, "/widgets/"+id+"/translations",
		`{"language":"kk","name":"Қазақша","description":"жарқырайды"}`, http.StatusOK)

	// GET by id with X-Language: kk -> translated in place.
	kk := getWithLang(t, srv, "/widgets/"+id, "kk")
	if kk["name"] != "Қазақша" || kk["description"] != "жарқырайды" {
		t.Fatalf("kk read not translated: %v", kk)
	}

	// GET by id without a language header -> canonical (default ru) content.
	def := mustDo(t, srv, http.MethodGet, "/widgets/"+id, "", http.StatusOK)
	if def["name"] != "gadget" || def["description"] != "shiny" {
		t.Fatalf("default read should be canonical, got: %v", def)
	}

	// GET the list with X-Language: kk -> every item batch-translated.
	list := getListWithLang(t, srv, "/widgets", "kk")
	var found bool
	for _, w := range list {
		if w["id"] == id {
			found = true
			if w["name"] != "Қазақша" {
				t.Fatalf("list item not translated: %v", w)
			}
		}
	}
	if !found {
		t.Fatalf("created widget %s not in list", id)
	}
}

// getWithLang issues a GET with an X-Language header and decodes a JSON object.
func getWithLang(t *testing.T, srv *httptest.Server, path, lang string) map[string]any {
	t.Helper()
	var out map[string]any
	doLangReq(t, srv, path, lang, &out)
	return out
}

// getListWithLang issues a GET with an X-Language header and returns the items
// of the paginated list envelope.
func getListWithLang(t *testing.T, srv *httptest.Server, path, lang string) []map[string]any {
	t.Helper()
	var out struct {
		Items []map[string]any `json:"items"`
	}
	doLangReq(t, srv, path, lang, &out)
	return out.Items
}

// doLangReq performs a GET with the X-Language header and decodes the JSON body.
func doLangReq(t *testing.T, srv *httptest.Server, path, lang string, v any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Language", lang)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s (lang=%s): %v", path, lang, err)
	}
	assertStatus(t, resp, http.StatusOK)
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}
