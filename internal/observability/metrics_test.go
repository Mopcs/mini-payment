package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestOutboxMetricsTrackRunningJobs(t *testing.T) {
	metrics := NewOutboxMetrics(prometheus.NewRegistry())

	metrics.StartJob()
	assertGauge(t, metrics.RunningJobs, 1)

	metrics.FinishJob()
	assertGauge(t, metrics.RunningJobs, 0)
}

// The worker may run without metrics, so every recorder has to survive a nil
// receiver instead of forcing nil checks into the publishing loop.
func TestMetricRecordersAcceptNilReceiver(t *testing.T) {
	var outbox *OutboxMetrics
	outbox.RecordClaimed(3)
	outbox.RecordPublished()
	outbox.RecordPublishFailure()
	outbox.RecordRetry()
	outbox.SetOldestPending(time.Minute)
	outbox.StartJob()
	outbox.FinishJob()

	var consumer *ConsumerMetrics
	consumer.RecordDelivery()
	consumer.RecordProcessing(time.Second, true, nil)
	consumer.RecordDeadLettered()
}
