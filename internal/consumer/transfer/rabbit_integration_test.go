package transfer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rabbitmq/amqp091-go"
)

type rabbitTestProcessor struct{ attempts atomic.Int32 }

func (p *rabbitTestProcessor) Process(_ context.Context, _ uuid.UUID, eventType string, _ []byte) (bool, error) {
	if eventType == "temporary.test" && p.attempts.Add(1) == 1 {
		return false, errors.New("temporary database failure")
	}
	if eventType == "permanent.test" {
		return false, ErrPermanent
	}
	return false, nil
}

func TestRabbitMQDeliveryIntegration(t *testing.T) {
	brokerURL := os.Getenv("TEST_RABBITMQ_URL")
	if brokerURL == "" {
		t.Skip("set TEST_RABBITMQ_URL to a dedicated RabbitMQ test broker")
	}
	if devURL := os.Getenv("RABBITMQ_URL"); devURL != "" && sameBrokerURL(brokerURL, devURL) {
		t.Fatal("TEST_RABBITMQ_URL must use a different broker from RABBITMQ_URL")
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	settings := Settings{
		URL: brokerURL, Exchange: "consumer.test." + suffix, Queue: "consumer.test." + suffix,
		RoutingKey: "events", RetryExchange: "consumer.retry." + suffix, RetryQueue: "consumer.retry." + suffix,
		DeadLetterExchange: "consumer.dlx." + suffix, DeadLetterQueue: "consumer.dlq." + suffix,
		DeadLetterRoutingKey: "failed", RetryDelay: 100 * time.Millisecond, MaxRetries: 1,
		Prefetch: 2, ReconnectInterval: time.Second,
	}
	processor := &rabbitTestProcessor{}
	consumer, err := NewWithProcessor(settings, processor, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	connection, err := amqp091.Dial(brokerURL)
	if err != nil {
		t.Fatalf("connect to test broker: %v", err)
	}
	defer connection.Close()
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if err := channel.Confirm(false); err != nil {
		t.Fatal(err)
	}
	confirms := channel.NotifyPublish(make(chan amqp091.Confirmation, 1))
	returns := channel.NotifyReturn(make(chan amqp091.Return, 1))

	ctx, cancel := context.WithCancel(context.Background())
	consumerDone := make(chan error, 1)
	go func() { consumerDone <- consumer.consumeSession(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-consumerDone:
		case <-time.After(5 * time.Second):
			t.Error("consumer did not stop")
		}
		cleanupRabbitTopology(t, brokerURL, settings)
	})
	waitForConsumer(t, brokerURL, settings.Queue)

	for _, eventType := range []string{"temporary.test", "permanent.test"} {
		id := uuid.NewString()
		if err := channel.PublishWithContext(ctx, settings.Exchange, settings.RoutingKey, true, false, amqp091.Publishing{
			ContentType: "application/json", DeliveryMode: amqp091.Persistent, MessageId: id,
			Headers: amqp091.Table{"event_type": eventType}, Body: []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case confirmation := <-confirms:
			if !confirmation.Ack {
				t.Fatal("broker rejected test message")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("test publish was not confirmed")
		}
		// A returned message would otherwise surface only as a confusing timeout.
		select {
		case returned := <-returns:
			t.Fatalf("test message was unroutable (code %d): %s",
				returned.ReplyCode, returned.ReplyText)
		default:
		}
		if eventType == "temporary.test" {
			waitFor(t, 5*time.Second, func() bool { return processor.attempts.Load() >= 2 })
		} else {
			dead := waitForDeadLetter(t, channel, settings.DeadLetterQueue)
			if dead.MessageId != id || dead.Headers["x-failure-reason"] != "permanent event error" {
				t.Fatalf("unexpected dead letter: id=%q headers=%v", dead.MessageId, dead.Headers)
			}
			if err := dead.Ack(false); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// waitForConsumer blocks until the queue exists and a consumer is attached.
// Waiting for the queue alone is not enough: the consumer declares the queue
// before it creates the bindings and starts consuming, so an early publish is
// returned as unroutable and the test never sees the message.
func waitForConsumer(t *testing.T, brokerURL, queue string) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		conn, err := amqp091.Dial(brokerURL)
		if err != nil {
			return false
		}
		defer conn.Close()
		ch, err := conn.Channel()
		if err != nil {
			return false
		}
		defer ch.Close()
		state, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
		return err == nil && state.Consumers > 0
	})
}

func waitForDeadLetter(t *testing.T, ch *amqp091.Channel, queue string) amqp091.Delivery {
	t.Helper()
	var result amqp091.Delivery
	waitFor(t, 5*time.Second, func() bool {
		delivery, ok, err := ch.Get(queue, false)
		if err != nil || !ok {
			return false
		}
		result = delivery
		return true
	})
	return result
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for broker state")
}

func cleanupRabbitTopology(t *testing.T, brokerURL string, settings Settings) {
	t.Helper()
	conn, err := amqp091.Dial(brokerURL)
	if err != nil {
		t.Errorf("connect for test topology cleanup: %v", err)
		return
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Errorf("open cleanup channel: %v", err)
		return
	}
	defer ch.Close()
	for _, queue := range []string{settings.Queue, settings.RetryQueue, settings.DeadLetterQueue} {
		if _, err := ch.QueueDelete(queue, false, false, false); err != nil {
			t.Errorf("delete test queue: %v", err)
		}
	}
	for _, exchange := range []string{settings.Exchange, settings.RetryExchange, settings.DeadLetterExchange} {
		if err := ch.ExchangeDelete(exchange, false, false); err != nil {
			t.Errorf("delete test exchange: %v", err)
		}
	}
}

func sameBrokerURL(a, b string) bool {
	left, errLeft := url.Parse(a)
	right, errRight := url.Parse(b)
	return errLeft == nil && errRight == nil && left.Scheme == right.Scheme && left.Host == right.Host && left.EscapedPath() == right.EscapedPath()
}
