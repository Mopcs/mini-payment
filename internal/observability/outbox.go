package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type OutboxMetrics struct {
	EventsClaimed   prometheus.Counter
	EventsPublished prometheus.Counter
	PublishFailures prometheus.Counter
	RetryAttempts   prometheus.Counter
	RunningJobs     prometheus.Gauge
	OldestPending   prometheus.Gauge
}

func NewOutboxMetrics(registry *prometheus.Registry) *OutboxMetrics {
	metrics := &OutboxMetrics{
		EventsClaimed: newCounter(
			"payment_outbox_events_claimed_total", "Outbox events claimed for publishing.",
		),
		EventsPublished: newCounter(
			"payment_outbox_events_published_total", "Outbox events confirmed by RabbitMQ.",
		),
		PublishFailures: newCounter(
			"payment_outbox_publish_failures_total", "Outbox publish attempts that failed.",
		),
		RetryAttempts: newCounter(
			"payment_outbox_retry_attempts_total", "Outbox retries successfully scheduled.",
		),
		RunningJobs: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "payment_outbox_jobs_running",
			Help: "Outbox events currently being published.",
		}),
		OldestPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "payment_outbox_oldest_pending_age_seconds",
			Help: "Age of the oldest unpublished outbox event.",
		}),
	}
	registry.MustRegister(
		metrics.EventsClaimed,
		metrics.EventsPublished,
		metrics.PublishFailures,
		metrics.RetryAttempts,
		metrics.RunningJobs,
		metrics.OldestPending,
	)
	return metrics
}

func newCounter(name, help string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
}

// The recorders below are nil-safe so the worker can run without metrics and
// still keep its publishing loop free of nil checks.

func (m *OutboxMetrics) RecordClaimed(count int) {
	if m != nil {
		m.EventsClaimed.Add(float64(count))
	}
}

func (m *OutboxMetrics) RecordPublished() {
	if m != nil {
		m.EventsPublished.Inc()
	}
}

func (m *OutboxMetrics) RecordPublishFailure() {
	if m != nil {
		m.PublishFailures.Inc()
	}
}

func (m *OutboxMetrics) RecordRetry() {
	if m != nil {
		m.RetryAttempts.Inc()
	}
}

func (m *OutboxMetrics) SetOldestPending(age time.Duration) {
	if m != nil {
		m.OldestPending.Set(age.Seconds())
	}
}

// StartJob and FinishJob bracket one event being published. They are nil-safe,
// and split in two so the call site can defer the finish on its own line.
func (m *OutboxMetrics) StartJob() {
	if m != nil {
		m.RunningJobs.Inc()
	}
}

func (m *OutboxMetrics) FinishJob() {
	if m != nil {
		m.RunningJobs.Dec()
	}
}
