package tests

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/assanoff/skit-x/internal/app/server"
)

// collectorConfig receives OTLP/gRPC and writes every trace export to
// /out/traces.json, one OTLP/JSON request per line.
const collectorConfig = `
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
exporters:
  file:
    path: /out/traces.json
    flush_interval: 100ms
service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [file]
`

// TestTracesReachCollector checks the export chain of the service end to end:
// with OTEL_TRACES_EXPORTER=otlp the server span of a request reaches a real
// collector over OTLP/gRPC, under the name of the service.
func TestTracesReachCollector(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	ctx := context.Background()
	collector, endpoint := startCollector(ctx, t)
	cfg := startPostgres(ctx, t)
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+endpoint)
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "100") // ms: export the span at once
	handler, err := server.Handler(ctx, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp := doReq(t, srv, http.MethodGet, "/widgets", "")
	_ = resp.Body.Close()
	assertStatus(t, resp, http.StatusOK)
	traceID := traceIDOf(t, resp.Header.Get("Traceparent"))

	exported := eventually(15*time.Second, 200*time.Millisecond, func() bool {
		return exportedSpan(ctx, t, collector, cfg.Service, traceID, "GET /widgets")
	})
	if !exported {
		t.Fatalf("collector has no span GET /widgets of trace %s from service %q", traceID, cfg.Service)
	}
}

// startCollector runs an OpenTelemetry Collector with collectorConfig and
// returns it with the host:port of its OTLP/gRPC receiver.
func startCollector(ctx context.Context, t *testing.T) (*testcontainers.DockerContainer, string) {
	t.Helper()
	c, err := testcontainers.Run(ctx, "otel/opentelemetry-collector-contrib:0.161.0",
		testcontainers.WithFiles(
			testcontainers.ContainerFile{
				Reader:            strings.NewReader(collectorConfig),
				ContainerFilePath: "/etc/otelcol-contrib/config.yaml",
				FileMode:          0o644,
			},
			// The collector runs as a non-root user: it can write to a
			// world-writable file in a directory it does not own.
			testcontainers.ContainerFile{
				Reader:            strings.NewReader(""),
				ContainerFilePath: "/out/traces.json",
				FileMode:          0o666,
			},
		),
		testcontainers.WithExposedPorts("4317/tcp"),
		testcontainers.WithWaitStrategy(wait.ForLog("Everything is ready")),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("start collector: %v", err)
	}
	endpoint, err := c.PortEndpoint(ctx, "4317/tcp", "")
	if err != nil {
		t.Fatalf("collector endpoint: %v", err)
	}
	return c, endpoint
}

// traceIDOf returns the trace id of a W3C traceparent header.
func traceIDOf(t *testing.T, traceparent string) string {
	t.Helper()
	parts := strings.Split(traceparent, "-")
	if len(parts) != 4 {
		t.Fatalf("traceparent = %q, want version-traceid-spanid-flags", traceparent)
	}
	return parts[1]
}

// otlpTraces is the part of an OTLP/JSON trace export the test reads.
type otlpTraces struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otlpAttribute `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []struct {
				TraceID string `json:"traceId"`
				Name    string `json:"name"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

// otlpAttribute is a string attribute of OTLP/JSON.
type otlpAttribute struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

// exportedSpan reports whether the collector wrote a span name of traceID
// whose Resource has service.name service.
func exportedSpan(ctx context.Context, t *testing.T, c *testcontainers.DockerContainer, service, traceID, name string) bool {
	t.Helper()
	rc, err := c.CopyFileFromContainer(ctx, "/out/traces.json")
	if err != nil {
		t.Fatalf("read collector output: %v", err)
	}
	defer func() { _ = rc.Close() }()
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read collector output: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		var export otlpTraces
		if json.Unmarshal([]byte(line), &export) != nil {
			continue
		}
		for _, rs := range export.ResourceSpans {
			if serviceName(rs.Resource.Attributes) != service {
				continue
			}
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					if s.TraceID == traceID && s.Name == name {
						return true
					}
				}
			}
		}
	}
	return false
}

// serviceName returns the service.name of a Resource.
func serviceName(attrs []otlpAttribute) string {
	for _, a := range attrs {
		if a.Key == "service.name" {
			return a.Value.StringValue
		}
	}
	return ""
}
