package transfer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/observability"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/rabbitmq/amqp091-go"
)

type testProcessor struct {
	err       error
	called    bool
	duplicate bool
}

func (p *testProcessor) Process(context.Context, uuid.UUID, string, []byte) (bool, error) {
	p.called = true
	return p.duplicate, p.err
}

type testPublisher struct {
	exchange, key string
	message       amqp091.Publishing
	err           error
	called        bool
}

func (p *testPublisher) Publish(_ context.Context, exchange, key string, message amqp091.Publishing) error {
	p.called = true
	p.exchange = exchange
	p.key = key
	p.message = message
	return p.err
}

type testAcknowledger struct {
	acked     bool
	published *testPublisher
	processor *testProcessor
}

func (a *testAcknowledger) Ack(uint64, bool) error {
	if a.processor != nil && !a.processor.called {
		return errors.New("ack before processing")
	}
	if a.published != nil && !a.published.called {
		return errors.New("ack before publish")
	}
	a.acked = true
	return nil
}
func (*testAcknowledger) Nack(uint64, bool, bool) error { return nil }
func (*testAcknowledger) Reject(uint64, bool) error     { return nil }

func testConsumer(t *testing.T, p Processor) *Consumer {
	t.Helper()
	settings := Settings{
		URL:                  "amqp://localhost",
		Exchange:             "main",
		Queue:                "main",
		RoutingKey:           "transfer.completed",
		RetryExchange:        "retry",
		RetryQueue:           "retry",
		DeadLetterExchange:   "dead",
		DeadLetterQueue:      "dead",
		DeadLetterRoutingKey: "failed",
		RetryDelay:           time.Second,
		MaxRetries:           2,
		Prefetch:             1,
		ReconnectInterval:    time.Second,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := NewWithProcessor(settings, p, logger)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHandleSchedulesConfirmedRetryBeforeAck(t *testing.T) {
	processor := &testProcessor{err: errors.New("database unavailable")}
	c := testConsumer(t, processor)
	publisher := &testPublisher{}
	acker := &testAcknowledger{published: publisher}
	d := amqp091.Delivery{Acknowledger: acker, DeliveryTag: 4, MessageId: uuid.NewString(), Headers: amqp091.Table{"event_type": "transfer.completed", "x-retry-count": int32(0)}, Body: []byte(`{}`)}
	if err := c.handle(context.Background(), d, publisher); err != nil {
		t.Fatal(err)
	}
	if !acker.acked || publisher.exchange != "retry" || publisher.key != "transfer.completed" || publisher.message.Headers["x-retry-count"] != int32(1) || publisher.message.DeliveryMode != amqp091.Persistent {
		t.Fatalf("retry was not persisted before ack: ack=%t exchange=%q headers=%v", acker.acked, publisher.exchange, publisher.message.Headers)
	}
}

func TestHandleRoutesPermanentFailureToDeadLetter(t *testing.T) {
	processor := &testProcessor{err: errors.Join(ErrPermanent, errors.New("invalid"))}
	c := testConsumer(t, processor)
	registry := prometheus.NewRegistry()
	c.metrics = observability.NewConsumerMetrics(registry)
	publisher := &testPublisher{}
	acker := &testAcknowledger{published: publisher}
	d := amqp091.Delivery{Acknowledger: acker, DeliveryTag: 5, MessageId: uuid.NewString(), Headers: amqp091.Table{"event_type": "invalid"}, Body: []byte("not logged")}
	if err := c.handle(context.Background(), d, publisher); err != nil {
		t.Fatal(err)
	}
	if !acker.acked || publisher.exchange != "dead" || publisher.key != "failed" || publisher.message.Headers["x-failure-reason"] != "permanent event error" {
		t.Fatalf("permanent message was not dead-lettered: %+v", publisher)
	}
	assertMetric(t, c.metrics.MessagesReceived, 1)
	assertMetric(t, c.metrics.ProcessingFailures, 1)
	assertMetric(t, c.metrics.DeadLettered, 1)
}

func TestHandleMovesExhaustedMessageToDeadLetter(t *testing.T) {
	processor := &testProcessor{err: errors.New("database unavailable")}
	c := testConsumer(t, processor)
	publisher := &testPublisher{}
	acker := &testAcknowledger{published: publisher}
	d := amqp091.Delivery{Acknowledger: acker, DeliveryTag: 6, MessageId: uuid.NewString(), Headers: amqp091.Table{"event_type": "transfer.completed", "x-retry-count": int32(2)}}
	if err := c.handle(context.Background(), d, publisher); err != nil {
		t.Fatal(err)
	}
	if !acker.acked || publisher.exchange != "dead" || publisher.message.Headers["x-failure-reason"] != "retry limit reached" {
		t.Fatalf("exhausted message was not dead-lettered: %+v", publisher)
	}
}

func TestHandleLeavesMessageUnackedWhenForwardingFails(t *testing.T) {
	processor := &testProcessor{err: errors.New("database unavailable")}
	c := testConsumer(t, processor)
	publisher := &testPublisher{err: errors.New("broker unavailable")}
	acker := &testAcknowledger{}
	d := amqp091.Delivery{Acknowledger: acker, DeliveryTag: 7, MessageId: uuid.NewString(), Headers: amqp091.Table{"event_type": "transfer.completed"}}
	if err := c.handle(context.Background(), d, publisher); err == nil {
		t.Fatal("expected publish error")
	}
	if acker.acked {
		t.Fatal("original message was acknowledged without a confirmed copy")
	}
}

func TestHandleAcknowledgesOnlyAfterSuccessfulProcessing(t *testing.T) {
	processor := &testProcessor{}
	c := testConsumer(t, processor)
	acker := &testAcknowledger{processor: processor}
	d := amqp091.Delivery{Acknowledger: acker, DeliveryTag: 8, MessageId: uuid.NewString(), Headers: amqp091.Table{"event_type": "transfer.completed"}}
	if err := c.handle(context.Background(), d, &testPublisher{}); err != nil {
		t.Fatal(err)
	}
	if !processor.called || !acker.acked {
		t.Fatalf("processing/ack state: processed=%t acked=%t", processor.called, acker.acked)
	}
}

func TestConsumerMetricsTrackDuplicateAndFailedDeliveries(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewConsumerMetrics(registry)
	processor := &testProcessor{duplicate: true}
	c := testConsumer(t, processor)
	c.metrics = metrics
	message := amqp091.Delivery{
		Acknowledger: &testAcknowledger{processor: processor},
		MessageId:    uuid.NewString(),
		Headers:      amqp091.Table{"event_type": "transfer.completed"},
	}
	if err := c.handle(context.Background(), message, &testPublisher{}); err != nil {
		t.Fatal(err)
	}

	processor.err = errors.New("database unavailable")
	publisher := &testPublisher{}
	message.Acknowledger = &testAcknowledger{published: publisher}
	if err := c.handle(context.Background(), message, publisher); err != nil {
		t.Fatal(err)
	}
	assertMetric(t, metrics.MessagesReceived, 2)
	assertMetric(t, metrics.MessagesProcessed, 1)
	assertMetric(t, metrics.DuplicateEvents, 1)
	assertMetric(t, metrics.ProcessingFailures, 1)
	assertMetric(t, metrics.DeadLettered, 0)
	name := "payment_consumer_processing_duration_seconds"
	if got := histogramSampleCount(t, registry, name); got != 2 {
		t.Fatalf("processing histogram count = %d, want 2", got)
	}
}

func TestMalformedAndUnsupportedEventsGoToDeadLetter(t *testing.T) {
	for _, tc := range []struct {
		name, eventType string
		body            []byte
	}{
		{name: "malformed JSON", eventType: "transfer.completed", body: []byte("{")},
		{name: "unsupported event", eventType: "transfer.failed", body: []byte(`{}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			processor := &transferProcessor{}
			c := testConsumer(t, processor)
			publisher := &testPublisher{}
			acker := &testAcknowledger{published: publisher}
			d := amqp091.Delivery{Acknowledger: acker, DeliveryTag: 9, MessageId: uuid.NewString(), Headers: amqp091.Table{"event_type": tc.eventType}, Body: tc.body}
			if err := c.handle(context.Background(), d, publisher); err != nil {
				t.Fatal(err)
			}
			if !acker.acked || publisher.exchange != "dead" {
				t.Fatalf("permanent event was not dead-lettered: ack=%t exchange=%q", acker.acked, publisher.exchange)
			}
		})
	}
}

func TestRetryCountValidation(t *testing.T) {
	for _, tc := range []struct {
		input any
		want  int
		ok    bool
	}{{nil, 0, true}, {int32(3), 3, true}, {"4", 4, true}, {int32(-1), 0, false}, {"not-a-number", 0, false}} {
		got, err := retryCount(tc.input)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("retryCount(%v) = %d, %v", tc.input, got, err)
		}
	}
}

func assertMetric(t *testing.T, metric prometheus.Metric, want float64) {
	t.Helper()
	data := &dto.Metric{}
	if err := metric.Write(data); err != nil {
		t.Fatal(err)
	}
	value := data.Counter.GetValue()
	if data.Gauge != nil {
		value = data.Gauge.GetValue()
	}
	if value != want {
		t.Fatalf("metric value = %v, want %v", value, want)
	}
}

func histogramSampleCount(t *testing.T, registry *prometheus.Registry, name string) uint64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return family.Metric[0].GetHistogram().GetSampleCount()
		}
	}
	t.Fatalf("histogram %q is not registered", name)
	return 0
}
