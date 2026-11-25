package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

const (
	outboxTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	outboxSpanID  = "00f067aa0ba902b7"
)

func TestOutboxPersistsTraceContext(t *testing.T) {
	pool := integrationPool(t)

	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample())))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})

	ctx := contextWithTestTrace(t)
	repository := NewOutboxRepository(pool)

	event := domain.OutboxEvent{
		ID:            uuid.New(),
		AggregateID:   uuid.New(),
		AggregateType: "transfer",
		EventType:     domain.EventTypeTransferCompleted,
		Payload: domain.TransferCompletedPayload{
			TransferID:          uuid.New(),
			SourceWalletID:      uuid.New(),
			DestinationWalletID: uuid.New(),
			Amount:              100,
			Currency:            "USD",
			CompletedAt:         time.Now().UTC(),
		},
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if err := repository.Create(ctx, tx, event); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("create outbox event: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}

	traceParent := storedTraceParent(t, ctx, pool, event.ID)
	if !strings.Contains(traceParent, outboxTraceID) {
		t.Fatalf("stored trace_parent = %q, want the trace that wrote the event", traceParent)
	}

	claimed := claimedEvent(t, ctx, repository, event.ID)
	if claimed.TraceParent != traceParent {
		t.Fatalf("claimed trace_parent = %q, want %q", claimed.TraceParent, traceParent)
	}
}

func contextWithTestTrace(t *testing.T) context.Context {
	t.Helper()

	traceID, err := trace.TraceIDFromHex(outboxTraceID)
	if err != nil {
		t.Fatalf("parse trace ID: %v", err)
	}
	spanID, err := trace.SpanIDFromHex(outboxSpanID)
	if err != nil {
		t.Fatalf("parse span ID: %v", err)
	}

	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithSpanContext(context.Background(), spanContext)
}

func storedTraceParent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	eventID uuid.UUID,
) string {
	t.Helper()

	var traceParent, traceState string
	err := pool.QueryRow(
		ctx,
		`SELECT trace_parent, trace_state FROM outbox_events WHERE id = $1`,
		eventID,
	).Scan(&traceParent, &traceState)
	if err != nil {
		t.Fatalf("read stored trace context: %v", err)
	}
	return traceParent
}

func claimedEvent(
	t *testing.T,
	ctx context.Context,
	repository *OutboxRepository,
	eventID uuid.UUID,
) domain.OutboxEvent {
	t.Helper()

	events, err := repository.ClaimBatch(ctx, uuid.New(), 100, time.Minute)
	if err != nil {
		t.Fatalf("claim outbox events: %v", err)
	}
	for _, event := range events {
		if event.ID == eventID {
			return event
		}
	}
	t.Fatalf("claimed batch does not contain event %s", eventID)
	return domain.OutboxEvent{}
}
