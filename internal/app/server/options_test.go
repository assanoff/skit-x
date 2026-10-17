package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/assanoff/skit/httpserver"
	"github.com/assanoff/skit/rest/router"

	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/deps"
)

// probe stands in for Install: a route set without the business handlers and the
// database behind them.
func probe(_ context.Context, _ *App, r *router.Router) {
	r.Handle("GET /probe", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		// An empty 200 is all the probe needs.
	}))
}

// setPaths is one path per route set, enough to tell whether a server serves it.
var setPaths = []struct{ set, path string }{
	{"health", "/healthz"},
	{"metrics", "/metrics"},
	{"pprof", "/debug/pprof/"},
	{"info", "/debug/components"},
	{"probe", "/probe"},
}

// WithServer adds exactly the server it describes: on the address it was given,
// serving the route sets it was given — nothing is added or moved for it.
func TestWithServer(t *testing.T) {
	type srv struct {
		addr string
		sets []string
	}
	debug := []string{"health", "metrics", "pprof", "info"}

	tests := []struct {
		name    string
		options []Option
		want    []srv
	}{
		{
			name:    "the API and the technical sets on their own ports",
			options: []Option{WithServer(":8080", probe), WithServer(":6060", Debug)},
			want: []srv{
				{addr: ":8080", sets: []string{"probe"}},
				{addr: ":6060", sets: debug},
			},
		},
		{
			name:    "one port",
			options: []Option{WithServer(":8080", Debug, probe)},
			want: []srv{
				{addr: ":8080", sets: slices.Concat(debug, []string{"probe"})},
			},
		},
		{
			name: "a port per set",
			options: []Option{
				WithServer(":8080", probe),
				WithServer(":9100", Metrics),
				WithServer(":6060", Health, Pprof, Info),
			},
			want: []srv{
				{addr: ":8080", sets: []string{"probe"}},
				{addr: ":9100", sets: []string{"metrics"}},
				{addr: ":6060", sets: []string{"health", "pprof", "info"}},
			},
		},
		{
			name:    "an empty address adds no server",
			options: []Option{WithServer("", Debug), WithGRPC(""), WithGateway(":8081", "")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestApp(t, tt.options...)

			var got []srv
			for i, s := range a.servers {
				h := handlerOf(t, a, i)
				l := srv{addr: s.Addr()}
				for _, p := range setPaths {
					if serves(h, p.path) {
						l.sets = append(l.sets, p.set)
					}
				}
				got = append(got, l)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("servers = %v, want %v", got, tt.want)
			}
		})
	}
}

// /debug/components lists where the process listens, one address per server,
// whichever server serves it.
func TestDebugComponents(t *testing.T) {
	a := newTestApp(t, WithServer(":8080", probe), WithServer(":6060", Info))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/components", nil)
	handlerOf(t, a, 1).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got Components
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := Components{Servers: []string{":8080", ":6060"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("components = %+v, want %+v", got, want)
	}
}

// newTestApp builds the app from options without starting it. The container is
// empty: initDeps would connect to Postgres (initAuditLog provisions its schema),
// and the route sets under test resolve nothing from it at build time.
func newTestApp(t *testing.T, options ...Option) *App {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	opts := config.ServerOpts{Service: "test"}
	a := newApp(opts, log, &deps.Deps{Opts: opts, Logger: log})
	for _, add := range options {
		if err := add(context.Background(), a); err != nil {
			t.Fatalf("option: %v", err)
		}
	}
	return a
}

func handlerOf(t *testing.T, a *App, i int) http.Handler {
	t.Helper()

	s, ok := a.servers[i].(*httpserver.Server)
	if !ok {
		t.Fatalf("server %d is %T", i, a.servers[i])
	}
	return s.Handler()
}

// serves reports whether h has a route for a GET of path.
func serves(h http.Handler, path string) bool {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code != http.StatusNotFound
}
