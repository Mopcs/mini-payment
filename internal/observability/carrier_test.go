package observability

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestAMQPHeadersCarryTraceContext(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

	spanContext := testSpanContext(t)
	ctx := trace.ContextWithSpanContext(context.Background(), spanContext)

	headers := AMQPHeaders{"event_type": "transfer.completed"}
	otel.GetTextMapPropagator().Inject(ctx, headers)

	traceParent := headers.Get("traceparent")
	if !strings.Contains(traceParent, spanContext.TraceID().String()) {
		t.Fatalf("traceparent = %q, want the injected trace ID", traceParent)
	}
	if headers.Get("event_type") != "transfer.completed" {
		t.Fatal("injection overwrote an existing header")
	}

	extracted := trace.SpanContextFromContext(
		otel.GetTextMapPropagator().Extract(context.Background(), headers),
	)
	if extracted.TraceID() != spanContext.TraceID() {
		t.Fatalf("extracted trace ID = %s, want %s", extracted.TraceID(), spanContext.TraceID())
	}
}

func TestAMQPHeadersIgnoreNonStringValues(t *testing.T) {
	headers := AMQPHeaders{"x-retry-count": int32(3)}

	if got := headers.Get("x-retry-count"); got != "" {
		t.Fatalf("Get() = %q, want an empty string for a non-string header", got)
	}
}
