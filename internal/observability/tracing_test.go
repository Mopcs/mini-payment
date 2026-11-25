package observability

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStartTracingDisabledReturnsNoopShutdown(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	shutdown, err := StartTracing(
		context.Background(), TracingSettings{Enabled: false}, discardLogger(),
	)
	if err != nil {
		t.Fatalf("StartTracing() error = %v", err)
	}
	if shutdown == nil {
		t.Fatal("StartTracing() returned a nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown error = %v", err)
	}
	if otel.GetTracerProvider() != previous {
		t.Fatal("disabled tracing replaced the global tracer provider")
	}
}

func TestStartTracingEnabledInstallsProvider(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})

	// Nothing listens on this port: the OTLP exporter connects lazily, so
	// initialisation must still succeed without a collector.
	shutdown, err := StartTracing(context.Background(), TracingSettings{
		Enabled:     true,
		ServiceName: "trace-test",
		Endpoint:    "127.0.0.1:4317",
		Insecure:    true,
		Sampler:     "always_on",
		SamplerArg:  1,
	}, discardLogger())
	if err != nil {
		t.Fatalf("StartTracing() error = %v", err)
	}

	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatal("global tracer provider was not replaced")
	}
	if _, ok := otel.GetTextMapPropagator().(propagation.TraceContext); !ok {
		t.Fatal("global propagator is not W3C trace context")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
}

func TestSamplerDecisions(t *testing.T) {
	tests := []struct {
		name  string
		ratio float64
		want  sdktrace.SamplingDecision
	}{
		{name: "always_on", ratio: 0, want: sdktrace.RecordAndSample},
		{name: "always_off", ratio: 1, want: sdktrace.Drop},
		{name: "traceidratio", ratio: 0, want: sdktrace.Drop},
		{name: "traceidratio", ratio: 1, want: sdktrace.RecordAndSample},
	}

	for _, test := range tests {
		sampler := NewSampler(test.name, test.ratio)
		decision := sampler.ShouldSample(sdktrace.SamplingParameters{})
		if decision.Decision != test.want {
			t.Fatalf("%s(%v) decision = %v, want %v",
				test.name, test.ratio, decision.Decision, test.want)
		}
	}
}
