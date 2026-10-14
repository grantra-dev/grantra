package telemetry

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"

	"github.com/grantra-dev/grantra/core/internal/accounts"
)

// HTTPHandler traces every request and records the standard HTTP server
// metrics. Health checks are left out: probes would drown everything else.
func HTTPHandler(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "grantra",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/readyz"
		}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method
		}),
	)
}

// TraceJob wraps one pass of a background job in a span named after it and
// records its outcome and duration.
func (m *Metrics) TraceJob(name string, run func(context.Context) error) func(context.Context) error {
	tracer := otel.Tracer(Name)
	job := attribute.String("job", name)
	return func(ctx context.Context) error {
		ctx, span := tracer.Start(ctx, "job "+name)
		defer span.End()
		start := time.Now()
		err := run(ctx)
		outcome := "ok"
		if err != nil {
			outcome = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		m.JobDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(job))
		m.JobRuns.Add(ctx, 1, metric.WithAttributes(job, attribute.String("outcome", outcome)))
		return err
	}
}

// RecordConnectorRun counts an aggregation or provisioning run and the
// counts it reports.
func (m *Metrics) RecordConnectorRun(ctx context.Context, r accounts.Run) {
	op := attribute.String("operation", r.Kind)
	m.ConnectorRuns.Add(ctx, 1, metric.WithAttributes(op, attribute.String("status", r.Status)))
	for name, n := range r.Counts {
		m.ConnectorChanges.Add(ctx, int64(n), metric.WithAttributes(op, attribute.String("count", name)))
	}
}
