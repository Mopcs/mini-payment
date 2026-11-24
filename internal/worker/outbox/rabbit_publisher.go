package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/rabbitmq/amqp091-go"
)

type RabbitSettings struct {
	URL        string
	Exchange   string
	Queue      string
	RoutingKey string
}

type RabbitPublisher struct {
	connection *amqp091.Connection
	channel    *amqp091.Channel
	confirms   <-chan amqp091.Confirmation
	returns    <-chan amqp091.Return
	settings   RabbitSettings
	mu         sync.Mutex
}

func NewRabbitPublisher(ctx context.Context, settings RabbitSettings) (*RabbitPublisher, error) {
	if settings.URL == "" ||
		settings.Exchange == "" ||
		settings.Queue == "" ||
		settings.RoutingKey == "" {
		return nil, errors.New("RabbitMQ URL, exchange, queue, and routing key are required")
	}

	connection, err := dialRabbit(ctx, settings.URL)
	if err != nil {
		return nil, err
	}

	channel, err := connection.Channel()
	if err != nil {
		_ = connection.Close()
		return nil, errors.New("open RabbitMQ channel failed")
	}

	if err := declarePublisherTopology(channel, settings); err != nil {
		_ = channel.Close()
		_ = connection.Close()
		return nil, err
	}

	return &RabbitPublisher{
		connection: connection,
		channel:    channel,
		confirms:   channel.NotifyPublish(make(chan amqp091.Confirmation, 1)),
		returns:    channel.NotifyReturn(make(chan amqp091.Return, 1)),
		settings:   settings,
	}, nil
}

func dialRabbit(ctx context.Context, url string) (*amqp091.Connection, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	connection, err := amqp091.DialConfig(url, amqp091.Config{
		Heartbeat: 10 * time.Second,
		Locale:    "en_US",
		Dial: func(network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		},
	})
	if err != nil {
		return nil, errors.New("connect to RabbitMQ failed")
	}
	return connection, nil
}

func declarePublisherTopology(channel *amqp091.Channel, settings RabbitSettings) error {
	err := channel.ExchangeDeclare(
		settings.Exchange, "direct", true, false, false, false, nil,
	)
	if err != nil {
		return fmt.Errorf("declare RabbitMQ exchange: %w", err)
	}

	queue, err := channel.QueueDeclare(settings.Queue, true, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("declare RabbitMQ queue: %w", err)
	}

	err = channel.QueueBind(queue.Name, settings.RoutingKey, settings.Exchange, false, nil)
	if err != nil {
		return fmt.Errorf("bind RabbitMQ queue: %w", err)
	}

	// Publisher confirms make Publish block until the broker has the message.
	if err := channel.Confirm(false); err != nil {
		return errors.New("enable RabbitMQ publisher confirms failed")
	}
	return nil
}

func (p *RabbitPublisher) Publish(ctx context.Context, event domain.OutboxEvent) error {
	// amqp channels are not safe for concurrent use, so serialise publishes.
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.connection == nil || p.channel == nil || p.connection.IsClosed() {
		return errors.New("RabbitMQ publisher connection is closed")
	}

	body, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}

	if err := p.send(ctx, event, body); err != nil {
		return err
	}
	if err := p.awaitConfirm(ctx); err != nil {
		return err
	}
	return p.checkReturn()
}

func (p *RabbitPublisher) send(
	ctx context.Context,
	event domain.OutboxEvent,
	body []byte,
) error {
	// mandatory=true asks the broker to return the message instead of dropping it
	// when nothing is bound to the routing key.
	err := p.channel.PublishWithContext(
		ctx,
		p.settings.Exchange,
		p.settings.RoutingKey,
		true,
		false,
		amqp091.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp091.Persistent,
			MessageId:    event.ID.String(),
			Headers:      amqp091.Table{"event_type": event.EventType},
			Body:         body,
		},
	)
	if err != nil {
		return fmt.Errorf("publish to RabbitMQ failed: %w", err)
	}
	return nil
}

func (p *RabbitPublisher) awaitConfirm(ctx context.Context) error {
	select {
	case confirmation, ok := <-p.confirms:
		if !ok {
			return errors.New("RabbitMQ publisher confirmation channel closed")
		}
		if !confirmation.Ack {
			return errors.New("RabbitMQ rejected publisher confirmation")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// checkReturn reports an unroutable message, which RabbitMQ sends before the
// publisher confirmation.
func (p *RabbitPublisher) checkReturn() error {
	select {
	case returned, ok := <-p.returns:
		if ok {
			return fmt.Errorf(
				"RabbitMQ returned unroutable message (reply code %d)",
				returned.ReplyCode,
			)
		}
	default:
	}
	return nil
}

func (p *RabbitPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.connection == nil {
		return nil
	}

	var result error
	if p.channel != nil {
		if err := p.channel.Close(); err != nil && !p.connection.IsClosed() {
			result = errors.Join(result, err)
		}
		p.channel = nil
	}
	if err := p.connection.Close(); err != nil && !p.connection.IsClosed() {
		result = errors.Join(result, err)
	}
	p.connection = nil
	return result
}
