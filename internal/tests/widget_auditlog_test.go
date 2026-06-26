package tests

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/assanoff/skit/auditlog"
	auditdb "github.com/assanoff/skit/auditlog/db"
	"github.com/assanoff/skit/logger"

	"github.com/assanoff/skit-x/core/widget"
)

// TestWidgetAuditLog exercises the auditlog demo over the full HTTP stack: the
// auditrest middleware records a versioned snapshot after each successful widget
// mutation (the WidgetResponse implements auditlog.Auditable). It asserts that a
// create + a real update produce versions 1 and 2, an identical update is deduped
// (no version 3), and the diff between versions captures the change.
func TestWidgetAuditLog(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	srv, cfg := newTestServer(ctx, t)
	db := openTestDB(t, cfg)
	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})
	auditCore := auditlog.NewCore(log, auditdb.NewStore(log, db))

	// Create -> audit version 1.
	created := mustDo(t, srv, http.MethodPost, "/widgets", `{"name":"gadget","description":"shiny"}`, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("expected an id, got %v", created)
	}

	// Real update -> audit version 2.
	mustDo(t, srv, http.MethodPut, "/widgets/"+id, `{"description":"updated"}`, http.StatusOK)

	// Identical update -> deduped, no new version.
	mustDo(t, srv, http.MethodPut, "/widgets/"+id, `{"description":"updated"}`, http.StatusOK)

	hist, err := auditCore.QueryHistoryByModelID(ctx, widget.AuditModelType, id)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 audit versions, got %d: %+v", len(hist), hist)
	}
	if hist[0].Version != 1 || hist[1].Version != 2 {
		t.Fatalf("versions = %d,%d, want 1,2", hist[0].Version, hist[1].Version)
	}

	// Diff between v1 and v2 captures the description change.
	var filter auditlog.QueryFilter
	filter.WithCurrentVersion(1)
	filter.WithTargetVersion(2)
	diff, err := auditCore.QueryDiffByModelID(ctx, widget.AuditModelType, id, filter)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(diff, "updated") && !strings.Contains(diff, "shiny") {
		t.Fatalf("diff did not capture the description change: %q", diff)
	}

	// Read API (auditrest handler group): history endpoint returns both versions.
	histResp := doReq(t, srv, http.MethodGet, "/auditlog/widget/"+id, "")
	assertStatus(t, histResp, http.StatusOK)
	var apiHist []map[string]any
	decode(t, histResp, &apiHist)
	if len(apiHist) != 2 {
		t.Fatalf("history API: expected 2 versions, got %d", len(apiHist))
	}

	// Read API: diff endpoint between v1 and v2.
	apiDiff := mustDo(t, srv, http.MethodGet, "/auditlog/widget/"+id+"/diff?current=1&target=2", "", http.StatusOK)
	if ds, _ := apiDiff["diff"].(string); ds == "" {
		t.Fatalf("diff API: empty diff: %v", apiDiff)
	}

	// Admin: on-demand compaction endpoint runs a batch (here below threshold, so
	// nothing is removed) and returns a JSON summary.
	compact := mustDo(t, srv, http.MethodPost, "/auditlog/compact", "", http.StatusOK)
	if _, ok := compact["deleted"]; !ok {
		t.Fatalf("compact API: missing 'deleted' in %v", compact)
	}
}
