package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
)

var ErrPermanent = errors.New("permanent event error")

type Settings struct {
	URL, Exchange, Queue, RoutingKey                          string
	RetryExchange, RetryQueue                                 string
	DeadLetterExchange, DeadLetterQueue, DeadLetterRoutingKey string
	RetryDelay                                                time.Duration
	MaxRetries, Prefetch                                      int
	ReconnectInterval                                         time.Duration
}

type Processor interface {
	Process(context.Context, uuid.UUID, string, []byte) (bool, error)
}
type publisher interface {
	Publish(context.Context, string, string, amqp091.Publishing) error
}

type Consumer struct {
	settings  Settings
	processor Processor
	logger    *slog.Logger
}

func New(pool *pgxpool.Pool, settings Settings, logger *slog.Logger) (*Consumer, error) {
	if pool == nil || logger == nil {
		return nil, errors.New("consumer requires a PostgreSQL pool and logger")
	}
	return NewWithProcessor(settings, &transferProcessor{repo: postgres.NewNotificationRepository(pool)}, logger)
}

func NewWithProcessor(settings Settings, processor Processor, logger *slog.Logger) (*Consumer, error) {
	if processor == nil || logger == nil || settings.URL == "" || settings.Exchange == "" || settings.Queue == "" || settings.RoutingKey == "" || settings.RetryExchange == "" || settings.RetryQueue == "" || settings.DeadLetterExchange == "" || settings.DeadLetterQueue == "" || settings.DeadLetterRoutingKey == "" || settings.RetryDelay < time.Millisecond || settings.RetryDelay.Milliseconds() > 1<<31-1 || settings.MaxRetries < 0 || settings.Prefetch < 1 || settings.ReconnectInterval <= 0 {
		return nil, errors.New("invalid transfer consumer configuration")
	}
	return &Consumer{settings: settings, processor: processor, logger: logger}, nil
}

func (c *Consumer) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := c.consumeSession(ctx); err != nil && ctx.Err() == nil {
			c.logger.Warn("consumer connection ended", slog.Any("error", err))
		}
		if ctx.Err() == nil {
			timer := time.NewTimer(c.settings.ReconnectInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	return nil
}

func (c *Consumer) consumeSession(ctx context.Context) error {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := amqp091.DialConfig(c.settings.URL, amqp091.Config{Heartbeat: 10 * time.Second, Locale: "en_US", Dial: func(network, address string) (net.Conn, error) { return dialer.DialContext(ctx, network, address) }})
	if err != nil {
		return errors.New("connect to RabbitMQ failed")
	}
	defer conn.Close()
	consumeCh, err := conn.Channel()
	if err != nil {
		return errors.New("open consumer channel failed")
	}
	defer consumeCh.Close()
	pubCh, err := conn.Channel()
	if err != nil {
		return errors.New("open publisher channel failed")
	}
	defer pubCh.Close()
	if err := c.declareTopology(consumeCh); err != nil {
		return fmt.Errorf("declare consumer topology: %w", err)
	}
	if err := consumeCh.Qos(c.settings.Prefetch, 0, false); err != nil {
		return errors.New("configure consumer prefetch failed")
	}
	if err := pubCh.Confirm(false); err != nil {
		return errors.New("enable publisher confirms failed")
	}
	p := &confirmedPublisher{channel: pubCh, confirms: pubCh.NotifyPublish(make(chan amqp091.Confirmation, 1)), returns: pubCh.NotifyReturn(make(chan amqp091.Return, 1))}
	deliveries, err := consumeCh.Consume(c.settings.Queue, "", false, false, false, false, nil)
	if err != nil {
		return errors.New("start RabbitMQ consumer failed")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("RabbitMQ delivery channel closed")
			}
			if err := c.handle(ctx, d, p); err != nil {
				return err
			}
		}
	}
}

func (c *Consumer) declareTopology(ch *amqp091.Channel) error {
	for _, exchange := range []string{c.settings.Exchange, c.settings.RetryExchange, c.settings.DeadLetterExchange} {
		if err := ch.ExchangeDeclare(exchange, "direct", true, false, false, false, nil); err != nil {
			return err
		}
	}
	mainQ, err := ch.QueueDeclare(c.settings.Queue, true, false, false, false, nil)
	if err != nil {
		return err
	}
	retryQ, err := ch.QueueDeclare(c.settings.RetryQueue, true, false, false, false, amqp091.Table{"x-message-ttl": int32(c.settings.RetryDelay.Milliseconds()), "x-dead-letter-exchange": c.settings.Exchange, "x-dead-letter-routing-key": c.settings.RoutingKey})
	if err != nil {
		return err
	}
	deadQ, err := ch.QueueDeclare(c.settings.DeadLetterQueue, true, false, false, false, nil)
	if err != nil {
		return err
	}
	for _, b := range []struct{ q, key, ex string }{{mainQ.Name, c.settings.RoutingKey, c.settings.Exchange}, {retryQ.Name, c.settings.RoutingKey, c.settings.RetryExchange}, {deadQ.Name, c.settings.DeadLetterRoutingKey, c.settings.DeadLetterExchange}} {
		if err := ch.QueueBind(b.q, b.key, b.ex, false, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *Consumer) handle(ctx context.Context, d amqp091.Delivery, p publisher) error {
	eventID, _ := uuid.Parse(d.MessageId)
	eventType, _ := d.Headers["event_type"].(string)
	_, err := c.processor.Process(ctx, eventID, eventType, d.Body)
	if err == nil {
		c.logger.Info("event processed", slog.String("event_id", eventID.String()), slog.String("event_type", eventType))
		// Process returns only after the notification transaction commits.
		return d.Ack(false)
	}
	permanent := errors.Is(err, ErrPermanent) || errors.Is(err, postgres.ErrInvalidTransferEvent)
	count, headerErr := retryCount(d.Headers["x-retry-count"])
	if headerErr != nil {
		permanent = true
	}
	if permanent {
		c.logger.Warn("event sent to dead-letter queue", slog.String("event_id", eventID.String()), slog.String("event_type", eventType), slog.String("reason", "permanent event error"))
		return c.forwardAndAck(ctx, d, p, c.settings.DeadLetterExchange, c.settings.DeadLetterRoutingKey, "permanent event error")
	}
	if count >= c.settings.MaxRetries {
		c.logger.Warn("event sent to dead-letter queue", slog.String("event_id", eventID.String()), slog.String("event_type", eventType), slog.String("reason", "retry limit reached"))
		return c.forwardAndAck(ctx, d, p, c.settings.DeadLetterExchange, c.settings.DeadLetterRoutingKey, "retry limit reached")
	}
	headers := copyHeaders(d.Headers)
	headers["x-retry-count"] = int32(count + 1)
	msg := amqp091.Publishing{Headers: headers, ContentType: d.ContentType, ContentEncoding: d.ContentEncoding, DeliveryMode: amqp091.Persistent, MessageId: d.MessageId, Timestamp: time.Now(), Type: d.Type, Body: d.Body}
	if err := p.Publish(ctx, c.settings.RetryExchange, c.settings.RoutingKey, msg); err != nil {
		return fmt.Errorf("publish retry: %w", err)
	}
	c.logger.Warn("event retry scheduled", slog.String("event_id", eventID.String()), slog.Int("retry_count", count+1))
	return d.Ack(false)
}

func (c *Consumer) forwardAndAck(ctx context.Context, d amqp091.Delivery, p publisher, exchange, key, reason string) error {
	headers := copyHeaders(d.Headers)
	headers["x-failure-reason"] = reason
	msg := amqp091.Publishing{Headers: headers, ContentType: d.ContentType, DeliveryMode: amqp091.Persistent, MessageId: d.MessageId, Timestamp: time.Now(), Type: d.Type, Body: d.Body}
	if err := p.Publish(ctx, exchange, key, msg); err != nil {
		return fmt.Errorf("publish dead-letter: %w", err)
	}
	return d.Ack(false)
}

func retryCount(v any) (int, error) {
	switch n := v.(type) {
	case int32:
		if n < 0 {
			return 0, errors.New("negative retry count")
		}
		return int(n), nil
	case int:
		if n < 0 {
			return 0, errors.New("negative retry count")
		}
		return n, nil
	case string:
		i, e := strconv.Atoi(n)
		if e != nil || i < 0 {
			return 0, errors.New("invalid retry count")
		}
		return i, nil
	default:
		if v == nil {
			return 0, nil
		}
		return 0, errors.New("invalid retry count")
	}
}
func copyHeaders(in amqp091.Table) amqp091.Table {
	out := amqp091.Table{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

type confirmedPublisher struct {
	channel  *amqp091.Channel
	confirms <-chan amqp091.Confirmation
	returns  <-chan amqp091.Return
}

func (p *confirmedPublisher) Publish(ctx context.Context, exchange, key string, msg amqp091.Publishing) error {
	if err := p.channel.PublishWithContext(ctx, exchange, key, true, false, msg); err != nil {
		return err
	}
	select {
	case conf, ok := <-p.confirms:
		if !ok || !conf.Ack {
			return errors.New("message was not confirmed")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case ret, ok := <-p.returns:
		if ok {
			return fmt.Errorf("message was unroutable (%d)", ret.ReplyCode)
		}
	default:
	}
	return nil
}

type transferProcessor struct {
	repo *postgres.NotificationRepository
}

func (p *transferProcessor) Process(ctx context.Context, eventID uuid.UUID, eventType string, body []byte) (bool, error) {
	if eventID == uuid.Nil {
		return false, fmt.Errorf("%w: invalid message ID", ErrPermanent)
	}
	if eventType != domain.EventTypeTransferCompleted {
		return false, fmt.Errorf("%w: unsupported event type", ErrPermanent)
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var payload domain.TransferCompletedPayload
	if err := dec.Decode(&payload); err != nil {
		return false, fmt.Errorf("%w: malformed payload", ErrPermanent)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("%w: trailing payload data", ErrPermanent)
	}
	if payload.TransferID == uuid.Nil || payload.SourceWalletID == uuid.Nil || payload.DestinationWalletID == uuid.Nil || payload.SourceWalletID == payload.DestinationWalletID || payload.Amount <= 0 || payload.CompletedAt.IsZero() || (payload.Currency != "USD" && payload.Currency != "EUR" && payload.Currency != "JPY") {
		return false, fmt.Errorf("%w: invalid transfer fields", ErrPermanent)
	}
	duplicate, err := p.repo.ProcessTransferCompleted(ctx, eventID, eventType, payload)
	if errors.Is(err, postgres.ErrForeignKeyViolation) || errors.Is(err, postgres.ErrCheckViolation) {
		return false, fmt.Errorf("%w: event conflicts with persisted records", ErrPermanent)
	}
	return duplicate, err
}
