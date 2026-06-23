package tests

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/assanoff/servicekit/logger"

	"github.com/assanoff/service-kit-x/internal/app/server"
)

// TestWidgetErrorLocalization checks that error responses are localized from the
// Accept-Language header (i18n middleware + localizeErrors app middleware).
func TestWidgetErrorLocalization(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	ctx := context.Background()
	srv, _ := newTestServer(ctx, t)
	missing := "/widgets/" + uuid.NewString()

	// Default (no Accept-Language) -> English.
	en := doReq(t, srv, http.MethodGet, missing, "")
	assertStatus(t, en, http.StatusNotFound)
	var enBody map[string]any
	decode(t, en, &enBody)
	if got, _ := enBody["detail"].(string); got == "" || got[:6] != "widget" {
		t.Errorf("expected English detail, got %q", enBody["detail"])
	}

	// Accept-Language: ru -> Russian.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+missing, nil)
	req.Header.Set("Accept-Language", "ru")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, http.StatusNotFound)
	var ruBody map[string]any
	decode(t, resp, &ruBody)
	detail, _ := ruBody["detail"].(string)
	if detail == "" || !containsCyrillic(detail) {
		t.Errorf("expected Russian detail, got %q", detail)
	}
}

// TestWidgetAuth checks JWT auth + RBAC on widget writes while reads stay public.
func TestWidgetAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	ctx := context.Background()
	cfg := startPostgres(ctx, t)
	cfg.Auth.Enabled = true
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Auth.RequiredRole = "widget:write"
	cfg.HTTP.RequestTimeout = 10 * time.Second

	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})
	slog.SetDefault(log.Slog())
	handler, err := server.Handler(ctx, cfg, log)
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	const body = `{"name":"secured","description":"x"}`

	// Reads are public.
	assertStatus(t, doReq(t, srv, http.MethodGet, "/widgets", ""), http.StatusOK)

	// Write without a token -> 401.
	assertStatus(t, doReq(t, srv, http.MethodPost, "/widgets", body), http.StatusUnauthorized)

	// Write with a token lacking the role -> 403.
	assertStatus(t, postWithToken(t, srv, body, signToken(t, "test-secret", "viewer")), http.StatusForbidden)

	// Write with the required role -> 201.
	assertStatus(t, postWithToken(t, srv, body, signToken(t, "test-secret", "widget:write")), http.StatusCreated)
}

func postWithToken(t *testing.T, srv *httptest.Server, body, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/widgets", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /widgets: %v", err)
	}
	return resp
}

func signToken(t *testing.T, secret string, roles ...string) string {
	t.Helper()
	r := make([]any, len(roles))
	for i, role := range roles {
		r[i] = role
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "test-user",
		"roles": r,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

func containsCyrillic(s string) bool {
	for _, r := range s {
		if r >= 'а' && r <= 'я' {
			return true
		}
	}
	return false
}
