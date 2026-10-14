package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSetupOffWithoutEndpoint(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
		t.Setenv(k, "")
	}
	tel, err := Setup(context.Background(), "test")
	if err != nil || tel.LogHandler != nil {
		t.Fatalf("telemetry should be off: %v, %v", tel.LogHandler, err)
	}
}

func TestSetupExportsAllSignals(t *testing.T) {
	var mu sync.Mutex
	got := map[string]bool{}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path] = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)

	ctx := context.Background()
	tel, err := Setup(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	job := m.TraceJob("test job", func(ctx context.Context) error {
		slog.New(tel.LogHandler).InfoContext(ctx, "inside the job")
		return nil
	})
	if err := job(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		if !got[path] {
			t.Errorf("nothing exported to %s; got %v", path, got)
		}
	}
}
