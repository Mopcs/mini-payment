package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

func TestRetryDelayIsExponentialAndBounded(t *testing.T) {
	maximum := 5 * time.Second
	for attempt, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: maximum, 20: maximum} {
		if got := RetryDelay(attempt, maximum); got != want {
			t.Errorf("RetryDelay(%d) = %s, want %s", attempt, got, want)
		}
	}
	if got := RetryDelay(1, 250*time.Millisecond); got != 250*time.Millisecond {
		t.Fatalf("RetryDelay() with smaller cap = %s", got)
	}
}

func TestClaimBatchRecoversExpiredLeasesAndSkipsPublishedEvents(t *testing.T) {
	pool := workerTestPool(t)
	repository := postgres.NewOutboxRepository(pool)
	workerID := uuid.New()
	availableID := insertWorkerTestEvent(t, pool)
	expiredID := insertWorkerTestEvent(t, pool)
	activeID := insertWorkerTestEvent(t, pool)
	futureID := insertWorkerTestEvent(t, pool)
	publishedID := insertWorkerTestEvent(t, pool)
	unsupportedID := insertWorkerTestEvent(t, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE outbox_events SET locked_by='old-worker', locked_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, expiredID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE outbox_events SET locked_by='active-worker', locked_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, activeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE outbox_events SET next_attempt_at=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, futureID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE outbox_events SET published_at=clock_timestamp() WHERE id=$1`, publishedID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE outbox_events SET event_type='future.event' WHERE id=$1`, unsupportedID); err != nil {
		t.Fatal(err)
	}

	claimed, err := repository.ClaimBatch(context.Background(), workerID, 10, time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch() error = %v", err)
	}
	if len(claimed) != 2 || !containsEvent(claimed, availableID) || !containsEvent(claimed, expiredID) || containsEvent(claimed, unsupportedID) {
		t.Fatalf("claimed IDs = %v, want available and expired events", eventIDs(claimed))
	}
	secondWorkerEvents, err := repository.ClaimBatch(context.Background(), uuid.New(), 10, time.Minute)
	if err != nil || len(secondWorkerEvents) != 0 {
		t.Fatalf("active leases claimed again: events=%v err=%v", eventIDs(secondWorkerEvents), err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE outbox_events SET locked_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, expiredID); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.ClaimBatch(context.Background(), uuid.New(), 10, time.Minute)
	if err != nil || len(recovered) != 1 || recovered[0].ID != expiredID {
		t.Fatalf("expired lease recovery = %v err=%v", eventIDs(recovered), err)
	}
}

func TestConcurrentWorkersClaimDifferentEvents(t *testing.T) {
	pool := workerTestPool(t)
	firstID, secondID := insertWorkerTestEvent(t, pool), insertWorkerTestEvent(t, pool)
	repository := postgres.NewOutboxRepository(pool)
	start := make(chan struct{})
	type result struct {
		events []domain.OutboxEvent
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		workerID := uuid.New()
		go func() {
			<-start
			events, err := repository.ClaimBatch(context.Background(), workerID, 1, time.Minute)
			results <- result{events: events, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || len(first.events) != 1 || len(second.events) != 1 {
		t.Fatalf("concurrent claims = %+v and %+v", first, second)
	}
	if first.events[0].ID == second.events[0].ID || !sameUUIDSet([]uuid.UUID{first.events[0].ID, second.events[0].ID}, []uuid.UUID{firstID, secondID}) {
		t.Fatalf("workers claimed duplicate or unexpected events: %s, %s", first.events[0].ID, second.events[0].ID)
	}
}

func TestWorkerSchedulesRetryAndMarksConfirmedEvent(t *testing.T) {
	pool := workerTestPool(t)
	failingID, successfulID := insertWorkerTestEvent(t, pool), insertWorkerTestEvent(t, pool)
	publisher := &fakePublisher{failures: map[uuid.UUID]error{failingID: errors.New("broker unavailable")}}
	worker := newTestWorker(t, pool, publisher, 2)
	if reconnect := worker.processBatch(context.Background(), publisher); !reconnect {
		t.Fatal("failed publication did not request a RabbitMQ reconnect")
	}
	assertOutboxState(t, pool, successfulID, func(published bool, attempts int, lockedBy *string, lastError *string, nextAttempt time.Time) {
		if !published || attempts != 0 || lockedBy != nil || lastError != nil {
			t.Fatalf("confirmed event state: published=%t attempts=%d locked_by=%v last_error=%v", published, attempts, lockedBy, lastError)
		}
	})
	assertOutboxState(t, pool, failingID, func(published bool, attempts int, lockedBy *string, lastError *string, nextAttempt time.Time) {
		if published || attempts != 1 || lockedBy != nil || lastError == nil || *lastError != "broker unavailable" || !nextAttempt.After(time.Now()) {
			t.Fatalf("failed event state: published=%t attempts=%d locked_by=%v last_error=%v next_attempt=%s", published, attempts, lockedBy, lastError, nextAttempt)
		}
	})
}

func TestOutboxUpdatesRequireWorkerOwnership(t *testing.T) {
	pool := workerTestPool(t)
	eventID := insertWorkerTestEvent(t, pool)
	repository := postgres.NewOutboxRepository(pool)
	owner, other := uuid.New(), uuid.New()
	if events, err := repository.ClaimBatch(context.Background(), owner, 1, time.Minute); err != nil || len(events) != 1 || events[0].ID != eventID {
		t.Fatalf("claim event = %v err=%v", eventIDs(events), err)
	}
	if err := repository.MarkPublished(context.Background(), eventID, other); !errors.Is(err, postgres.ErrOutboxLeaseLost) {
		t.Fatalf("MarkPublished with wrong owner error = %v", err)
	}
	if err := repository.ScheduleRetry(context.Background(), eventID, other, "failure", time.Second); !errors.Is(err, postgres.ErrOutboxLeaseLost) {
		t.Fatalf("ScheduleRetry with wrong owner error = %v", err)
	}
	if err := repository.ExtendLease(context.Background(), eventID, other, time.Minute); !errors.Is(err, postgres.ErrOutboxLeaseLost) {
		t.Fatalf("ExtendLease with wrong owner error = %v", err)
	}
	if err := repository.ExtendLease(context.Background(), eventID, owner, time.Minute); err != nil {
		t.Fatalf("ExtendLease with owner: %v", err)
	}
	if err := repository.MarkPublished(context.Background(), eventID, owner); err != nil {
		t.Fatalf("MarkPublished with owner: %v", err)
	}
	if events, err := repository.ClaimBatch(context.Background(), uuid.New(), 1, time.Minute); err != nil || len(events) != 0 {
		t.Fatalf("published event was claimed again: %v err=%v", eventIDs(events), err)
	}
}

func TestConfirmedButUnmarkedEventCanBeRepublished(t *testing.T) {
	pool := workerTestPool(t)
	eventID := insertWorkerTestEvent(t, pool)
	publisher := &fakePublisher{}
	worker := newTestWorker(t, pool, publisher, 1)
	functionName := "test_fail_outbox_mark_" + strings.ReplaceAll(eventID.String(), "-", "")
	triggerName := "test_" + functionName
	_, err := pool.Exec(context.Background(), fmt.Sprintf("CREATE FUNCTION %s() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'test mark failure'; END; $$ LANGUAGE plpgsql", functionName))
	if err != nil {
		t.Fatalf("create mark failure function: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+triggerName+" ON outbox_events")
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION IF EXISTS "+functionName+"()")
	})
	if _, err := pool.Exec(context.Background(), fmt.Sprintf("CREATE TRIGGER %s BEFORE UPDATE ON outbox_events FOR EACH ROW WHEN (NEW.id = '%s'::uuid AND NEW.published_at IS NOT NULL AND OLD.published_at IS NULL) EXECUTE FUNCTION %s()", triggerName, eventID, functionName)); err != nil {
		t.Fatalf("create mark failure trigger: %v", err)
	}

	worker.processBatch(context.Background(), publisher)
	if got := publisher.publishedIDs(); len(got) != 1 || got[0] != eventID {
		t.Fatalf("first publication IDs = %v", got)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE outbox_events SET locked_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), "DROP TRIGGER "+triggerName+" ON outbox_events"); err != nil {
		t.Fatal(err)
	}
	worker.processBatch(context.Background(), publisher)
	if got := publisher.publishedIDs(); len(got) != 2 || got[0] != eventID || got[1] != eventID {
		t.Fatalf("republish IDs = %v, want same stable event ID twice", got)
	}
	assertOutboxState(t, pool, eventID, func(published bool, _ int, lockedBy *string, _ *string, _ time.Time) {
		if !published || lockedBy != nil {
			t.Fatalf("event was not marked after retry: published=%t locked_by=%v", published, lockedBy)
		}
	})
}

func TestWorkerShutdownClosesPublisher(t *testing.T) {
	pool := workerTestPool(t)
	publisher := &fakePublisher{}
	connected := make(chan struct{}, 1)
	worker, err := New(pool, testSettings(1), func(context.Context) (Publisher, error) {
		connected <- struct{}{}
		return publisher, nil
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	<-connected
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker shutdown error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	if !publisher.closed() {
		t.Fatal("worker shutdown did not close RabbitMQ publisher")
	}
}

func TestWorkerReconnectsAfterRabbitMQBecomesAvailable(t *testing.T) {
	pool := workerTestPool(t)
	publisher := &fakePublisher{}
	connectionAttempts := make(chan int, 2)
	var calls int
	settings := testSettings(1)
	settings.PollInterval = 10 * time.Millisecond
	worker, err := New(pool, settings, func(context.Context) (Publisher, error) {
		calls++
		connectionAttempts <- calls
		if calls == 1 {
			return nil, errors.New("broker unavailable")
		}
		return publisher, nil
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	if attempt := <-connectionAttempts; attempt != 1 {
		t.Fatalf("first connection attempt = %d", attempt)
	}
	select {
	case attempt := <-connectionAttempts:
		if attempt != 2 {
			t.Fatalf("second connection attempt = %d", attempt)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("worker did not retry the RabbitMQ connection")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker shutdown error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	if !publisher.closed() {
		t.Fatal("worker shutdown did not close the reconnected publisher")
	}
}

func TestRabbitPublisherIntegration(t *testing.T) {
	testURL := os.Getenv("TEST_RABBITMQ_URL")
	if testURL == "" {
		t.Skip("set TEST_RABBITMQ_URL to a dedicated RabbitMQ test broker")
	}
	settings := RabbitSettings{
		URL:        testURL,
		Exchange:   "outbox_test_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Queue:      "outbox_test_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		RoutingKey: "transfer.completed",
	}
	publisher, err := NewRabbitPublisher(context.Background(), settings)
	if err != nil {
		t.Fatalf("connect to test RabbitMQ: %v", err)
	}
	t.Cleanup(func() {
		_ = publisher.Close()
		connection, dialErr := amqp091.Dial(testURL)
		if dialErr != nil {
			return
		}
		defer connection.Close()
		channel, channelErr := connection.Channel()
		if channelErr != nil {
			return
		}
		defer channel.Close()
		_, _ = channel.QueueDelete(settings.Queue, false, false, false)
		_ = channel.ExchangeDelete(settings.Exchange, false, false)
	})
	event := sampleOutboxEvent()
	if err := publisher.Publish(context.Background(), event); err != nil {
		t.Fatalf("publish confirmed event: %v", err)
	}
	message, ok, err := publisher.channel.Get(settings.Queue, true)
	if err != nil || !ok {
		t.Fatalf("read published event from test queue: ok=%t err=%v", ok, err)
	}
	if message.MessageId != event.ID.String() || message.ContentType != "application/json" || message.DeliveryMode != amqp091.Persistent || message.Headers["event_type"] != event.EventType {
		t.Fatalf("AMQP message properties = %+v", message)
	}
	var payload domain.TransferCompletedPayload
	if err := json.Unmarshal(message.Body, &payload); err != nil || payload.TransferID != event.Payload.TransferID || payload.SourceWalletID != event.Payload.SourceWalletID || payload.DestinationWalletID != event.Payload.DestinationWalletID || payload.Amount != event.Payload.Amount || payload.Currency != event.Payload.Currency || !payload.CompletedAt.Equal(event.Payload.CompletedAt) {
		t.Fatalf("AMQP message payload = %+v err=%v", payload, err)
	}
	if _, err := publisher.channel.QueueDelete(settings.Queue, false, false, false); err != nil {
		t.Fatalf("remove test queue binding: %v", err)
	}
	if err := publisher.Publish(context.Background(), sampleOutboxEvent()); err == nil {
		t.Fatal("mandatory publish without a bound queue was treated as successful")
	}
}

type fakePublisher struct {
	mu       sync.Mutex
	failures map[uuid.UUID]error
	ids      []uuid.UUID
	closedCh bool
}

func (p *fakePublisher) Publish(_ context.Context, event domain.OutboxEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids = append(p.ids, event.ID)
	return p.failures[event.ID]
}

func (p *fakePublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closedCh = true
	return nil
}

func (p *fakePublisher) publishedIDs() []uuid.UUID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uuid.UUID(nil), p.ids...)
}

func (p *fakePublisher) closed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closedCh
}

func newTestWorker(t *testing.T, pool *pgxpool.Pool, publisher Publisher, batchSize int) *Worker {
	t.Helper()
	worker, err := New(pool, testSettings(batchSize), func(context.Context) (Publisher, error) { return publisher, nil }, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func testSettings(batchSize int) Settings {
	return Settings{BatchSize: batchSize, PollInterval: time.Hour, LeaseDuration: time.Minute, MaxBackoff: 5 * time.Minute}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func sampleOutboxEvent() domain.OutboxEvent {
	return domain.OutboxEvent{
		ID: uuid.New(), AggregateID: uuid.New(), AggregateType: "transfer", EventType: domain.EventTypeTransferCompleted,
		Payload: domain.TransferCompletedPayload{TransferID: uuid.New(), SourceWalletID: uuid.New(), DestinationWalletID: uuid.New(), Amount: 500, Currency: "USD", CompletedAt: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)},
	}
}

func insertWorkerTestEvent(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	event := sampleOutboxEvent()
	event.AggregateID = event.Payload.TransferID
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatalf("marshal test payload: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO outbox_events(id, aggregate_id, aggregate_type, event_type, payload) VALUES ($1,$2,$3,$4,$5::jsonb)`, event.ID, event.AggregateID, event.AggregateType, event.EventType, string(payload)); err != nil {
		t.Fatalf("insert test outbox event: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM outbox_events WHERE id=$1`, event.ID); err != nil {
			t.Errorf("delete test outbox event: %v", err)
		}
	})
	return event.ID
}

func assertOutboxState(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, assert func(bool, int, *string, *string, time.Time)) {
	t.Helper()
	var publishedAt *time.Time
	var lockedBy, lastError *string
	var attempts int
	var nextAttempt time.Time
	if err := pool.QueryRow(context.Background(), `SELECT published_at, attempts, locked_by, last_error, next_attempt_at FROM outbox_events WHERE id=$1`, id).Scan(&publishedAt, &attempts, &lockedBy, &lastError, &nextAttempt); err != nil {
		t.Fatalf("load outbox state: %v", err)
	}
	assert(publishedAt != nil, attempts, lockedBy, lastError, nextAttempt)
}

func workerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("OUTBOX_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set OUTBOX_TEST_DATABASE_URL to a dedicated PostgreSQL test database with migrations applied")
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse OUTBOX_TEST_DATABASE_URL: %v", err)
	}
	if !strings.HasSuffix(poolConfig.ConnConfig.Database, "_test") {
		t.Fatal("OUTBOX_TEST_DATABASE_URL must point to a dedicated database whose name ends in _test")
	}
	if mainURL := os.Getenv("TEST_DATABASE_URL"); mainURL != "" {
		mainConfig, err := pgxpool.ParseConfig(mainURL)
		if err != nil {
			t.Fatalf("parse TEST_DATABASE_URL: %v", err)
		}
		if mainConfig.ConnConfig.Host == poolConfig.ConnConfig.Host && mainConfig.ConnConfig.Port == poolConfig.ConnConfig.Port && mainConfig.ConnConfig.Database == poolConfig.ConnConfig.Database {
			t.Fatal("OUTBOX_TEST_DATABASE_URL must use a separate database from TEST_DATABASE_URL")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test database: %v", err)
	}
	var schemaReady bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.outbox_events') IS NOT NULL`).Scan(&schemaReady); err != nil || !schemaReady {
		pool.Close()
		t.Fatalf("outbox schema is not ready; apply migrations before integration tests: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func containsEvent(events []domain.OutboxEvent, id uuid.UUID) bool {
	for _, event := range events {
		if event.ID == id {
			return true
		}
	}
	return false
}

func eventIDs(events []domain.OutboxEvent) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}

func sameUUIDSet(left, right []uuid.UUID) bool {
	if len(left) != len(right) {
		return false
	}
	for _, id := range left {
		found := false
		for _, candidate := range right {
			found = found || id == candidate
		}
		if !found {
			return false
		}
	}
	return true
}
