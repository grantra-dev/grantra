package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

func TestObserveBacklog(t *testing.T) {
	pool := dbtest.Open(t)
	tenantID := dbtest.Tenant(t, pool, "backlog")

	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err := ObserveBacklog(pool, tenantID); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			seen[m.Name] = true
		}
	}
	for _, name := range []string{"grantra.access_requests.pending", "grantra.sod.violations", "grantra.notifications.queued"} {
		if !seen[name] {
			t.Errorf("%s not reported; got %v", name, seen)
		}
	}
}
