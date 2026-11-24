package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type ConsumerMetrics struct {
	MessagesReceived   prometheus.Counter
	MessagesProcessed  prometheus.Counter
	DuplicateEvents    prometheus.Counter
	ProcessingFailures prometheus.Counter
	DeadLettered       prometheus.Counter
	ProcessingDuration prometheus.Histogram
}

func NewConsumerMetrics(registry *prometheus.Registry) *ConsumerMetrics {
	metrics := &ConsumerMetrics{
		MessagesReceived: newCounter(
			"payment_consumer_messages_received_total", "RabbitMQ messages delivered to the consumer.",
		),
		MessagesProcessed: newCounter(
			"payment_consumer_messages_processed_total", "Messages processed successfully.",
		),
		DuplicateEvents: newCounter(
			"payment_consumer_duplicate_events_total", "Duplicate events ignored by the consumer.",
		),
		ProcessingFailures: newCounter(
			"payment_consumer_processing_failures_total", "Message processing attempts that failed.",
		),
		DeadLettered: newCounter(
			"payment_consumer_messages_dead_lettered_total", "Messages confirmed in the dead-letter queue.",
		),
		ProcessingDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "payment_consumer_processing_duration_seconds",
			Help:    "Message processing duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}),
	}
	registry.MustRegister(
		metrics.MessagesReceived,
		metrics.MessagesProcessed,
		metrics.DuplicateEvents,
		metrics.ProcessingFailures,
		metrics.DeadLettered,
		metrics.ProcessingDuration,
	)
	return metrics
}

func (m *ConsumerMetrics) RecordDelivery() {
	if m != nil {
		m.MessagesReceived.Inc()
	}
}

func (m *ConsumerMetrics) RecordProcessing(duration time.Duration, duplicate bool, err error) {
	if m == nil {
		return
	}
	m.ProcessingDuration.Observe(duration.Seconds())
	if err != nil {
		m.ProcessingFailures.Inc()
		return
	}
	m.MessagesProcessed.Inc()
	if duplicate {
		m.DuplicateEvents.Inc()
	}
}

func (m *ConsumerMetrics) RecordDeadLettered() {
	if m != nil {
		m.DeadLettered.Inc()
	}
}
