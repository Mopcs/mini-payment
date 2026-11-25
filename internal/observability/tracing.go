package observability

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerScope     = "mini-payment-system"
	shutdownTimeout = 5 * time.Second
)

// Tracer returns the tracer every component in this service shares. Package
// level tracers are safe: the OpenTelemetry proxy resolves the provider on the
// first span, which happens after StartTracing has run.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerScope)
}

// TracingSettings is the resolved tracing configuration for one process.
type TracingSettings struct {
	Enabled     bool
	ServiceName string
	Endpoint    string
	Insecure    bool
	Sampler     string
	SamplerArg  float64
}

// StartTracing installs the global tracer provider and propagator, and returns
// the function that flushes pending spans. Disabled tracing returns a no-op
// shutdown and leaves the globals alone, so the process runs without a
// collector.
func StartTracing(
	ctx context.Context,
	settings TracingSettings,
	logger *slog.Logger,
) (func(context.Context) error, error) {
	if !settings.Enabled {
		logger.Info("tracing disabled")
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx, exporterOptions(settings)...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}

	provider, err := newTracerProvider(ctx, settings, exporter)
	if err != nil {
		return nil, err
	}

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	// Export failures must not spam stderr; the batch processor already drops
	// spans when the collector is unreachable.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Debug("opentelemetry export failed", slog.Any("error", err))
	}))

	logger.Info(
		"tracing enabled",
		slog.String("service", settings.ServiceName),
		slog.String("endpoint", settings.Endpoint),
		slog.String("sampler", settings.Sampler),
	)
	return provider.Shutdown, nil
}

// ShutdownTracing flushes pending spans inside a bounded timeout.
func ShutdownTracing(shutdown func(context.Context) error, logger *slog.Logger) {
	if shutdown == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	logger.Info("flushing traces")
	if err := shutdown(ctx); err != nil {
		logger.Warn("flush traces failed", slog.Any("error", err))
	}
}

func exporterOptions(settings TracingSettings) []otlptracegrpc.Option {
	options := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(settings.Endpoint)}
	if settings.Insecure {
		options = append(options, otlptracegrpc.WithInsecure())
	}
	return options
}

func newTracerProvider(
	ctx context.Context,
	settings TracingSettings,
	exporter sdktrace.SpanExporter,
) (*sdktrace.TracerProvider, error) {
	attributes := resource.WithAttributes(semconv.ServiceNameKey.String(settings.ServiceName))
	res, err := resource.New(ctx, attributes)
	if err != nil {
		return nil, fmt.Errorf("build tracing resource: %w", err)
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(NewSampler(settings.Sampler, settings.SamplerArg)),
		sdktrace.WithBatcher(exporter),
	), nil
}

// NewSampler maps the OTEL_TRACES_SAMPLER names onto SDK samplers. An unknown
// name falls back to the parent-based ratio sampler, which is the safe default.
func NewSampler(name string, ratio float64) sdktrace.Sampler {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(ratio)
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	default:
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	}
}
