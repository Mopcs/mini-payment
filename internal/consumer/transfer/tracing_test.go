package transfer

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rabbitmq/amqp091-go"
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

func TestStartConsumeSpanLinksProducer(t *testing.T) {
	exporter := testSpanExporter(t)

	delivery := amqp091.Delivery{
		MessageId: uuid.NewString(),
		Headers: amqp091.Table{
			"event_type":  domain.EventTypeTransferCompleted,
			"traceparent": producerTraceParent,
		},
	}

	_, span := startConsumeSpan(
		context.Background(), delivery, uuid.New(), domain.EventTypeTransferCompleted,
	)
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}

	links := spans[0].Links
	if len(links) != 1 {
		t.Fatalf("span has %d links, want 1", len(links))
	}
	if got := links[0].SpanContext.TraceID().String(); got != producerTraceID {
		t.Fatalf("link trace ID = %s, want %s", got, producerTraceID)
	}

	// A link is used instead of a parent, so the consumer span does not inherit
	// the producer's timing across the queue delay.
	if spans[0].Parent.IsValid() {
		t.Fatal("consumer span should be a new root with a link, not a child span")
	}
}

func TestStartConsumeSpanWithoutTraceHeaders(t *testing.T) {
	exporter := testSpanExporter(t)

	delivery := amqp091.Delivery{
		MessageId: uuid.NewString(),
		Headers:   amqp091.Table{"event_type": domain.EventTypeTransferCompleted},
	}

	_, span := startConsumeSpan(
		context.Background(), delivery, uuid.New(), domain.EventTypeTransferCompleted,
	)
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if len(spans[0].Links) != 0 {
		t.Fatalf("span has %d links, want none", len(spans[0].Links))
	}
	if !spans[0].SpanContext.IsValid() {
		t.Fatal("consumer span has no trace context")
	}
}
