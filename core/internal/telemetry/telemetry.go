// Package telemetry sends Grantra's traces, metrics and logs through
// OpenTelemetry (OTLP over HTTP). It is off unless an OTLP endpoint is set
// with the standard variables, such as OTEL_EXPORTER_OTLP_ENDPOINT; every
// other OTEL_* variable (headers, service name, sampling...) works as usual.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Name is the instrumentation scope of Grantra's own spans and metrics.
const Name = "github.com/grantra-dev/grantra/core"

// Enabled reports whether an OTLP endpoint is configured.
func Enabled() bool {
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// Telemetry holds the providers Setup installed.
type Telemetry struct {
	// LogHandler sends log records to the collector; nil when telemetry is off.
	LogHandler slog.Handler
	shutdown   []func(context.Context) error
}

// Setup installs the global tracer and meter providers and returns a log
// handler for the collector. When telemetry is off it installs nothing, and
// the instrumentation throughout Grantra costs next to nothing.
func Setup(ctx context.Context, version string) (*Telemetry, error) {
	t := &Telemetry{}
	if !Enabled() {
		return t, nil
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", "grantra"),
			attribute.String("service.version", version),
		),
		resource.WithFromEnv(), // OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES win
		resource.WithTelemetrySDK(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, err
	}

	traces, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traces), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.shutdown = append(t.shutdown, tp.Shutdown)

	metrics, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics)), sdkmetric.WithResource(res))
	otel.SetMeterProvider(mp)
	t.shutdown = append(t.shutdown, mp.Shutdown)

	logs, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logs)), sdklog.WithResource(res))
	t.shutdown = append(t.shutdown, lp.Shutdown)
	t.LogHandler = otelslog.NewHandler(Name, otelslog.WithLoggerProvider(lp))
	return t, nil
}

// Shutdown flushes whatever is still buffered and stops the providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(t.shutdown) - 1; i >= 0; i-- {
		errs = append(errs, t.shutdown[i](ctx))
	}
	t.shutdown = nil
	return errors.Join(errs...)
}
