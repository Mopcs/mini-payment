package outbox

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

const (
	producerTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
	producerTraceParent = "00-" + producerTraceID + "-00f067aa0ba902b7-01"
)

func testSpanExporter(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	return exporter
}

func TestStartPublishSpanContinuesEventTrace(t *testing.T) {
	exporter := testSpanExporter(t)

	event := domain.OutboxEvent{
		ID:          uuid.New(),
		EventType:   domain.EventTypeTransferCompleted,
		TraceParent: producerTraceParent,
	}

	_, span := startPublishSpan(context.Background(), event)
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if got := spans[0].SpanContext.TraceID().String(); got != producerTraceID {
		t.Fatalf("trace ID = %s, want the stored trace %s", got, producerTraceID)
	}
}

// An event written before tracing existed has no stored context, so the publish
// has to start its own trace instead of failing.
func TestStartPublishSpanWithoutStoredContext(t *testing.T) {
	exporter := testSpanExporter(t)

	_, span := startPublishSpan(context.Background(), domain.OutboxEvent{
		ID:        uuid.New(),
		EventType: domain.EventTypeTransferCompleted,
	})
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if !spans[0].SpanContext.IsValid() {
		t.Fatal("publish span has no trace context")
	}
	if got := spans[0].SpanContext.TraceID().String(); got == producerTraceID {
		t.Fatal("publish span reused a trace it was never given")
	}
}
