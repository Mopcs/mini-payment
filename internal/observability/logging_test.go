package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

const (
	testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID  = "00f067aa0ba902b7"
)

func testSpanContext(t *testing.T) trace.SpanContext {
	t.Helper()

	traceID, err := trace.TraceIDFromHex(testTraceID)
	if err != nil {
		t.Fatalf("parse trace ID: %v", err)
	}
	spanID, err := trace.SpanIDFromHex(testSpanID)
	if err != nil {
		t.Fatalf("parse span ID: %v", err)
	}

	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
}

func decodeRecord(t *testing.T, buffer *bytes.Buffer) map[string]any {
	t.Helper()

	var record map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatalf("decode log record: %v", err)
	}
	return record
}

func TestLoggerAddsTraceFields(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(&traceHandler{Handler: slog.NewJSONHandler(&buffer, nil)})

	spanContext := testSpanContext(t)
	ctx := trace.ContextWithSpanContext(context.Background(), spanContext)
	logger.InfoContext(ctx, "message with trace")

	record := decodeRecord(t, &buffer)
	if record["trace_id"] != testTraceID {
		t.Fatalf("trace_id = %v, want %s", record["trace_id"], testTraceID)
	}
	if record["span_id"] != testSpanID {
		t.Fatalf("span_id = %v, want %s", record["span_id"], testSpanID)
	}
}

func TestLoggerOmitsTraceFieldsWithoutSpan(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(&traceHandler{Handler: slog.NewJSONHandler(&buffer, nil)})

	logger.Info("message without trace")

	record := decodeRecord(t, &buffer)
	if _, ok := record["trace_id"]; ok {
		t.Fatal("trace_id was added to a record without an active span")
	}
	if _, ok := record["span_id"]; ok {
		t.Fatal("span_id was added to a record without an active span")
	}
}
