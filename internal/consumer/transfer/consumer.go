package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/observability"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

// ErrPermanent marks an error that retrying cannot fix, so the message goes
// straight to the dead-letter queue.
var ErrPermanent = errors.New("permanent event error")

// maxQueueTTL is the largest x-message-ttl RabbitMQ accepts.
const maxQueueTTL = int64(1<<31 - 1)

// deliveryTimeout bounds one delivery, including the window after a shutdown
// signal. It is long enough for a transaction and short enough that a stuck
// handler cannot keep the process alive forever.
const deliveryTimeout = 15 * time.Second

// shutdownMessage is logged when the delivery loop stops on a signal.
const shutdownMessage = "shutdown requested, stopping delivery loop"

// Settings describes the topology the consumer expects. Everything except
// MaxRetries has to be set; NewWithProcessor rejects the rest.
type Settings struct {
	URL        string
	Exchange   string
	Queue      string
	RoutingKey string

	RetryExchange string
	RetryQueue    string

	DeadLetterExchange   string
	DeadLetterQueue      string
	DeadLetterRoutingKey string

	RetryDelay        time.Duration
	MaxRetries        int
	Prefetch          int
	ReconnectInterval time.Duration
	Metrics           *observability.ConsumerMetrics
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
	metrics   *observability.ConsumerMetrics
}

type deliveryFailure struct {
	eventID    uuid.UUID
	eventType  string
	retryCount int
	reason     string
	err        error
}

type forwardingTarget struct {
	exchange   string
	routingKey string
	reason     string
}

func New(pool *pgxpool.Pool, settings Settings, logger *slog.Logger) (*Consumer, error) {
	if pool == nil || logger == nil {
		return nil, errors.New("consumer requires a PostgreSQL pool and logger")
	}
	processor := &transferProcessor{repo: postgres.NewNotificationRepository(pool)}
	return NewWithProcessor(settings, processor, logger)
}

func NewWithProcessor(
	settings Settings,
	processor Processor,
	logger *slog.Logger,
) (*Consumer, error) {
	if processor == nil || logger == nil {
		return nil, errors.New("consumer requires a processor and logger")
	}
	if err := settings.validate(); err != nil {
		return nil, err
	}
	return &Consumer{
		settings:  settings,
		processor: processor,
		logger:    logger,
		metrics:   settings.Metrics,
	}, nil
}

func (s Settings) validate() error {
	switch {
	case s.URL == "" || s.Exchange == "" || s.Queue == "" || s.RoutingKey == "":
		return errors.New("consumer requires a RabbitMQ URL, exchange, queue and routing key")
	case s.RetryExchange == "" || s.RetryQueue == "":
		return errors.New("consumer requires a retry exchange and queue")
	case s.DeadLetterExchange == "" || s.DeadLetterQueue == "" || s.DeadLetterRoutingKey == "":
		return errors.New("consumer requires a dead-letter exchange, queue and routing key")
	case s.RetryDelay < time.Millisecond || s.RetryDelay.Milliseconds() > maxQueueTTL:
		return errors.New("consumer retry delay must be between 1ms and the queue TTL limit")
	case s.MaxRetries < 0:
		return errors.New("consumer max retries must not be negative")
	case s.Prefetch < 1:
		return errors.New("consumer prefetch must be at least 1")
	case s.ReconnectInterval <= 0:
		return errors.New("consumer reconnect interval must be positive")
	}
	return nil
}

// Run keeps a consuming session alive, reconnecting after every failure until
// the context is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := c.consumeSession(ctx); err != nil && ctx.Err() == nil {
			c.logger.Warn("consumer connection ended", slog.Any("error", err))
		}
		if !waitOrDone(ctx, c.settings.ReconnectInterval) {
			return nil
		}
	}
	return nil
}

// waitOrDone sleeps for the given duration and reports whether the context is
// still alive afterwards.
func waitOrDone(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Consumer) consumeSession(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return c.consumeConnection(ctx, conn)
}

func (c *Consumer) consumeConnection(
	ctx context.Context,
	conn *amqp091.Connection,
) error {
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
	return c.consumeChannels(ctx, consumeCh, pubCh)
}

func (c *Consumer) consumeChannels(
	ctx context.Context,
	consumeCh *amqp091.Channel,
	pubCh *amqp091.Channel,
) error {
	if err := c.declareTopology(consumeCh); err != nil {
		return fmt.Errorf("declare consumer topology: %w", err)
	}
	if err := consumeCh.Qos(c.settings.Prefetch, 0, false); err != nil {
		return errors.New("configure consumer prefetch failed")
	}

	publisher, err := newConfirmedPublisher(pubCh)
	if err != nil {
		return err
	}

	deliveries, err := consumeCh.Consume(c.settings.Queue, "", false, false, false, false, nil)
	if err != nil {
		return errors.New("start RabbitMQ consumer failed")
	}
	return c.consumeDeliveries(ctx, deliveries, publisher)
}

// consumeDeliveries runs the delivery loop until the context is cancelled. It
// handles one delivery at a time, so a shutdown signal can never interrupt a
// message that is already being processed.
func (c *Consumer) consumeDeliveries(
	ctx context.Context,
	deliveries <-chan amqp091.Delivery,
	publisher publisher,
) error {
	for {
		select {
		case <-ctx.Done():
			c.logger.InfoContext(ctx, shutdownMessage)
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("RabbitMQ delivery channel closed")
			}
			// A buffered delivery can win the select race against a signal.
			// Starting it anyway would mean work this process cannot finish,
			// so it stays unacked and is redelivered after the restart.
			if ctx.Err() != nil {
				c.logger.InfoContext(ctx, shutdownMessage)
				return nil
			}
			if err := c.handleDelivery(ctx, delivery, publisher); err != nil {
				return err
			}
		}
	}
}

// handleDelivery processes a single delivery under a bounded context that
// survives cancellation. Without this a shutdown signal would abort the
// notification transaction halfway and the message would be retried even
// though it was never at fault.
func (c *Consumer) handleDelivery(
	ctx context.Context,
	delivery amqp091.Delivery,
	publisher publisher,
) error {
	handleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryTimeout)
	defer cancel()
	return c.handle(handleCtx, delivery, publisher)
}

func (c *Consumer) dial(ctx context.Context) (*amqp091.Connection, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}

	conn, err := amqp091.DialConfig(c.settings.URL, amqp091.Config{
		Heartbeat: 10 * time.Second,
		Locale:    "en_US",
		Dial: func(network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		},
	})
	if err != nil {
		return nil, errors.New("connect to RabbitMQ failed")
	}
	return conn, nil
}

func newConfirmedPublisher(channel *amqp091.Channel) (*confirmedPublisher, error) {
	if err := channel.Confirm(false); err != nil {
		return nil, errors.New("enable publisher confirms failed")
	}
	return &confirmedPublisher{
		channel:  channel,
		confirms: channel.NotifyPublish(make(chan amqp091.Confirmation, 1)),
		returns:  channel.NotifyReturn(make(chan amqp091.Return, 1)),
	}, nil
}

type queueBinding struct {
	queue      string
	routingKey string
	exchange   string
}

func (c *Consumer) declareTopology(ch *amqp091.Channel) error {
	if err := c.declareExchanges(ch); err != nil {
		return err
	}
	bindings, err := c.declareQueues(ch)
	if err != nil {
		return err
	}
	return bindQueues(ch, bindings)
}

func (c *Consumer) declareExchanges(ch *amqp091.Channel) error {
	for _, exchange := range []string{
		c.settings.Exchange,
		c.settings.RetryExchange,
		c.settings.DeadLetterExchange,
	} {
		if err := ch.ExchangeDeclare(exchange, "direct", true, false, false, false, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *Consumer) declareQueues(ch *amqp091.Channel) ([]queueBinding, error) {
	mainQueue, err := ch.QueueDeclare(c.settings.Queue, true, false, false, false, nil)
	if err != nil {
		return nil, err
	}

	// The retry queue holds a message for RetryDelay and then dead-letters it
	// back onto the main exchange. That is what makes retries delayed.
	retryQueue, err := ch.QueueDeclare(c.settings.RetryQueue, true, false, false, false, amqp091.Table{
		"x-message-ttl":             int32(c.settings.RetryDelay.Milliseconds()),
		"x-dead-letter-exchange":    c.settings.Exchange,
		"x-dead-letter-routing-key": c.settings.RoutingKey,
	})
	if err != nil {
		return nil, err
	}

	deadLetterQueue, err := ch.QueueDeclare(c.settings.DeadLetterQueue, true, false, false, false, nil)
	if err != nil {
		return nil, err
	}

	bindings := []queueBinding{
		{queue: mainQueue.Name, routingKey: c.settings.RoutingKey, exchange: c.settings.Exchange},
		{queue: retryQueue.Name, routingKey: c.settings.RoutingKey, exchange: c.settings.RetryExchange},
		{
			queue:      deadLetterQueue.Name,
			routingKey: c.settings.DeadLetterRoutingKey,
			exchange:   c.settings.DeadLetterExchange,
		},
	}
	return bindings, nil
}

func bindQueues(ch *amqp091.Channel, bindings []queueBinding) error {
	for _, binding := range bindings {
		err := ch.QueueBind(
			binding.queue, binding.routingKey, binding.exchange, false, nil,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// startConsumeSpan opens the consumer span and links it to the producer trace.
// A link keeps the spans related without stretching the producer span across the
// time the message waited in the queue.
func startConsumeSpan(
	ctx context.Context,
	delivery amqp091.Delivery,
	eventID uuid.UUID,
	eventType string,
) (context.Context, trace.Span) {
	producer := otel.GetTextMapPropagator().Extract(
		ctx, observability.AMQPHeaders(delivery.Headers),
	)
	options := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("rabbitmq"),
			semconv.MessagingOperationNameKey.String("process"),
			semconv.MessagingMessageIDKey.String(eventID.String()),
			attribute.String("consumer.event_type", eventType),
		),
	}
	if producerSpan := trace.SpanContextFromContext(producer); producerSpan.IsValid() {
		options = append(options, trace.WithLinks(trace.Link{SpanContext: producerSpan}))
	}
	return observability.Tracer().Start(ctx, "consumer.process", options...)
}

// handle processes one delivery and always leaves it either acked or untouched,
// so a failed forward keeps the original message on the queue.
func (c *Consumer) handle(
	ctx context.Context,
	delivery amqp091.Delivery,
	publisher publisher,
) error {
	eventID, _ := uuid.Parse(delivery.MessageId)
	eventType, _ := delivery.Headers["event_type"].(string)

	ctx, span := startConsumeSpan(ctx, delivery, eventID, eventType)
	defer span.End()

	c.metrics.RecordDelivery()
	started := time.Now()
	duplicate, err := c.processor.Process(ctx, eventID, eventType, delivery.Body)
	c.metrics.RecordProcessing(time.Since(started), duplicate, err)

	if err == nil {
		span.SetAttributes(
			attribute.Bool("consumer.duplicate", duplicate),
			attribute.String("consumer.settle", "ack"),
		)
		c.logger.InfoContext(ctx, "event processed",
			slog.String("event_id", eventID.String()),
			slog.String("event_type", eventType))
		// Process returns only after the notification transaction commits.
		return delivery.Ack(false)
	}

	observability.RecordError(span, err)
	return c.handleFailure(ctx, delivery, publisher, deliveryFailure{
		eventID:   eventID,
		eventType: eventType,
		err:       err,
	})
}

func (c *Consumer) handleFailure(
	ctx context.Context,
	delivery amqp091.Delivery,
	publisher publisher,
	failure deliveryFailure,
) error {
	count, countErr := retryCount(delivery.Headers["x-retry-count"])

	switch {
	case countErr != nil,
		errors.Is(failure.err, ErrPermanent),
		errors.Is(failure.err, postgres.ErrInvalidTransferEvent):
		failure.reason = "permanent event error"
		return c.deadLetter(ctx, delivery, publisher, failure)
	case count >= c.settings.MaxRetries:
		failure.reason = "retry limit reached"
		return c.deadLetter(ctx, delivery, publisher, failure)
	default:
		failure.retryCount = count
		return c.scheduleRetry(ctx, delivery, publisher, failure)
	}
}

func (c *Consumer) deadLetter(
	ctx context.Context,
	delivery amqp091.Delivery,
	publisher publisher,
	failure deliveryFailure,
) error {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("consumer.settle", "dead_letter"))

	c.logger.WarnContext(ctx, "event sent to dead-letter queue",
		slog.String("event_id", failure.eventID.String()),
		slog.String("event_type", failure.eventType),
		slog.String("reason", failure.reason))

	return c.forwardAndAck(
		ctx, delivery, publisher,
		forwardingTarget{
			exchange:   c.settings.DeadLetterExchange,
			routingKey: c.settings.DeadLetterRoutingKey,
			reason:     failure.reason,
		},
	)
}

func (c *Consumer) scheduleRetry(
	ctx context.Context,
	delivery amqp091.Delivery,
	publisher publisher,
	failure deliveryFailure,
) error {
	headers := copyHeaders(delivery.Headers)
	headers["x-retry-count"] = int32(failure.retryCount + 1)

	message := amqp091.Publishing{
		Headers:         headers,
		ContentType:     delivery.ContentType,
		ContentEncoding: delivery.ContentEncoding,
		DeliveryMode:    amqp091.Persistent,
		MessageId:       delivery.MessageId,
		Timestamp:       time.Now(),
		Type:            delivery.Type,
		Body:            delivery.Body,
	}
	if err := publisher.Publish(
		ctx, c.settings.RetryExchange, c.settings.RoutingKey, message,
	); err != nil {
		return fmt.Errorf("publish retry: %w", err)
	}

	trace.SpanFromContext(ctx).SetAttributes(attribute.String("consumer.settle", "retry"))

	c.logger.WarnContext(ctx, "event retry scheduled",
		slog.String("event_id", failure.eventID.String()),
		slog.Int("retry_count", failure.retryCount+1))
	return delivery.Ack(false)
}

func (c *Consumer) forwardAndAck(
	ctx context.Context,
	delivery amqp091.Delivery,
	publisher publisher,
	target forwardingTarget,
) error {
	headers := copyHeaders(delivery.Headers)
	headers["x-failure-reason"] = target.reason

	message := amqp091.Publishing{
		Headers:      headers,
		ContentType:  delivery.ContentType,
		DeliveryMode: amqp091.Persistent,
		MessageId:    delivery.MessageId,
		Timestamp:    time.Now(),
		Type:         delivery.Type,
		Body:         delivery.Body,
	}
	if err := publisher.Publish(ctx, target.exchange, target.routingKey, message); err != nil {
		return fmt.Errorf("publish dead-letter: %w", err)
	}
	c.metrics.RecordDeadLettered()
	return delivery.Ack(false)
}

func retryCount(value any) (int, error) {
	switch n := value.(type) {
	case nil:
		return 0, nil
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
		count, err := strconv.Atoi(n)
		if err != nil || count < 0 {
			return 0, errors.New("invalid retry count")
		}
		return count, nil
	default:
		return 0, errors.New("invalid retry count")
	}
}

func copyHeaders(in amqp091.Table) amqp091.Table {
	out := make(amqp091.Table, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// confirmedPublisher publishes on a channel with publisher confirms enabled and
// waits for the broker to accept the message.
type confirmedPublisher struct {
	channel  *amqp091.Channel
	confirms <-chan amqp091.Confirmation
	returns  <-chan amqp091.Return
}

func (p *confirmedPublisher) Publish(
	ctx context.Context,
	exchange string,
	key string,
	message amqp091.Publishing,
) error {
	if err := p.channel.PublishWithContext(ctx, exchange, key, true, false, message); err != nil {
		return err
	}

	select {
	case confirmation, ok := <-p.confirms:
		if !ok || !confirmation.Ack {
			return errors.New("message was not confirmed")
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	// A returned message means nothing was bound to the routing key.
	select {
	case returned, ok := <-p.returns:
		if ok {
			return fmt.Errorf("message was unroutable (%d)", returned.ReplyCode)
		}
	default:
	}
	return nil
}

type transferProcessor struct {
	repo *postgres.NotificationRepository
}

// Process validates the message and records it. The bool result reports a
// duplicate delivery, which is still a success.
func (p *transferProcessor) Process(
	ctx context.Context,
	eventID uuid.UUID,
	eventType string,
	body []byte,
) (duplicate bool, returnedErr error) {
	ctx, span := observability.Tracer().Start(ctx, "consumer.decode")
	defer func() {
		observability.RecordError(span, returnedErr)
		span.End()
	}()

	if eventID == uuid.Nil {
		return false, fmt.Errorf("%w: invalid message ID", ErrPermanent)
	}
	if eventType != domain.EventTypeTransferCompleted {
		return false, fmt.Errorf("%w: unsupported event type", ErrPermanent)
	}

	payload, err := decodeTransferPayload(body)
	if err != nil {
		return false, err
	}
	if err := validateTransferPayload(payload); err != nil {
		return false, err
	}

	duplicate, err = p.repo.ProcessTransferCompleted(ctx, eventID, eventType, payload)
	if errors.Is(err, postgres.ErrForeignKeyViolation) || errors.Is(err, postgres.ErrCheckViolation) {
		return false, fmt.Errorf("%w: event conflicts with persisted records", ErrPermanent)
	}
	return duplicate, err
}

// decodeTransferPayload reads exactly one JSON object and rejects unknown
// fields, so a schema change cannot slip through unnoticed.
func decodeTransferPayload(body []byte) (domain.TransferCompletedPayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	var payload domain.TransferCompletedPayload
	if err := decoder.Decode(&payload); err != nil {
		return domain.TransferCompletedPayload{}, fmt.Errorf("%w: malformed payload", ErrPermanent)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domain.TransferCompletedPayload{}, fmt.Errorf("%w: trailing payload data", ErrPermanent)
	}
	return payload, nil
}

// validateTransferPayload rejects messages that cannot describe a real transfer
// before they reach the database.
func validateTransferPayload(payload domain.TransferCompletedPayload) error {
	switch {
	case payload.TransferID == uuid.Nil:
		return fmt.Errorf("%w: missing transfer ID", ErrPermanent)
	case payload.SourceWalletID == uuid.Nil || payload.DestinationWalletID == uuid.Nil:
		return fmt.Errorf("%w: missing wallet ID", ErrPermanent)
	case payload.SourceWalletID == payload.DestinationWalletID:
		return fmt.Errorf("%w: source and destination wallets match", ErrPermanent)
	case payload.Amount <= 0:
		return fmt.Errorf("%w: non-positive amount", ErrPermanent)
	case payload.CompletedAt.IsZero():
		return fmt.Errorf("%w: missing completion time", ErrPermanent)
	case !domain.IsSupportedCurrency(payload.Currency):
		return fmt.Errorf("%w: unsupported currency", ErrPermanent)
	}
	return nil
}
