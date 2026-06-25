package tests

import (
	"context"
	"net/http"
	"testing"
)

// TestWidgetJSONAPI exercises the JSON:API variant of the widget read endpoints,
// which return the jsonapi-tagged widget.Resource via the SDK's to.JSONAPI
// helper (github.com/hashicorp/jsonapi builds the document).
func TestWidgetJSONAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)

	created := mustDo(t, srv, http.MethodPost, "/widgets",
		`{"name":"jsonapi-gadget","description":"shiny"}`, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("create returned no id")
	}

	// Single resource: { "data": { "type", "id", "attributes" } }.
	doc := mustDo(t, srv, http.MethodGet, "/widgets/jsonapi/"+id, "", http.StatusOK)
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected a data object, got %T: %v", doc["data"], doc)
	}
	if data["type"] != "widgets" || data["id"] != id {
		t.Fatalf("data type/id = %v/%v, want widgets/%s", data["type"], data["id"], id)
	}
	attrs, _ := data["attributes"].(map[string]any)
	if attrs["name"] != "jsonapi-gadget" {
		t.Fatalf("attributes = %v", attrs)
	}

	// Collection: { "data": [ ... ] }.
	list := mustDo(t, srv, http.MethodGet, "/widgets/jsonapi", "", http.StatusOK)
	arr, ok := list["data"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("expected a 1-item data array, got %T (len %d)", list["data"], len(arr))
	}
}
