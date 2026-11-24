package transfer

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
	"github.com/Mopcs/mini-payment-system/internal/service/wallet"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConsumerDatabaseIntegration(t *testing.T) {
	pool := consumerTestPool(t)
	ctx := context.Background()
	users := postgres.NewUserRepository(pool)
	ownerA := &domain.User{Email: "consumer-" + uuid.NewString() + "@example.test", PasswordHash: "test-hash"}
	ownerB := &domain.User{Email: "consumer-" + uuid.NewString() + "@example.test", PasswordHash: "test-hash"}
	if err := users.Create(ctx, ownerA); err != nil {
		t.Fatal(err)
	}
	if err := users.Create(ctx, ownerB); err != nil {
		t.Fatal(err)
	}
	defer cleanupConsumerFixture(t, pool, ownerA.ID, ownerB.ID)
	wallets := wallet.NewService(pool)
	source, err := wallets.Create(ctx, ownerA.ID, "USD")
	if err != nil {
		t.Fatal(err)
	}
	destination, err := wallets.Create(ctx, ownerB.ID, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := wallets.Deposit(ctx, ownerA.ID, source.ID, 3000, "USD", "consumer-fund-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	transfer, replay, err := transferservice.NewService(pool).Transfer(ctx, ownerA.ID, source.ID, destination.ID, 1000, "USD", "consumer-transfer-"+uuid.NewString())
	if err != nil || replay {
		t.Fatalf("create transfer replay=%t err=%v", replay, err)
	}
	eventID, payload := loadConsumerEvent(t, pool, transfer.TransactionID)
	processor := &transferProcessor{repo: postgres.NewNotificationRepository(pool)}
	body, _ := json.Marshal(payload)
	if duplicate, err := processor.Process(ctx, eventID, domain.EventTypeTransferCompleted, body); err != nil || duplicate {
		t.Fatalf("process first delivery duplicate=%t err=%v", duplicate, err)
	}
	var notificationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event_id=$1`, eventID).Scan(&notificationCount); err != nil || notificationCount != 2 {
		t.Fatalf("notification count=%d err=%v", notificationCount, err)
	}
	rows, err := pool.Query(ctx, `SELECT user_id, payload FROM notifications WHERE event_id=$1`, eventID)
	if err != nil {
		t.Fatal(err)
	}
	directions := map[uuid.UUID]string{}
	for rows.Next() {
		var userID uuid.UUID
		var raw []byte
		if err := rows.Scan(&userID, &raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var notification domain.TransferNotificationPayload
		if err := json.Unmarshal(raw, &notification); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if notification.TransferID != transfer.TransactionID || notification.Amount != 1000 || notification.Currency != "USD" {
			rows.Close()
			t.Fatalf("unexpected notification payload: %+v", notification)
		}
		directions[userID] = notification.Direction
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if directions[ownerA.ID] != "outgoing" || directions[ownerB.ID] != "incoming" {
		t.Fatalf("notification directions = %v", directions)
	}
	if duplicate, err := processor.Process(ctx, eventID, domain.EventTypeTransferCompleted, body); err != nil || !duplicate {
		t.Fatalf("process replay duplicate=%t err=%v", duplicate, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event_id=$1`, eventID).Scan(&notificationCount); err != nil || notificationCount != 2 {
		t.Fatalf("replay notification count=%d err=%v", notificationCount, err)
	}

	failedEventID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,aggregate_id,aggregate_type,event_type,payload) SELECT $1,aggregate_id,'transfer',$2,payload FROM outbox_events WHERE id=$3`, failedEventID, domain.EventTypeTransferCompleted, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION test_consumer_notification_failure() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'test failure'; END; $$ LANGUAGE plpgsql`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS test_consumer_notification_failure ON notifications`)
		_, _ = pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS test_consumer_notification_failure()`)
	})
	if _, err := pool.Exec(ctx, `CREATE TRIGGER test_consumer_notification_failure BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION test_consumer_notification_failure()`); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.Process(ctx, failedEventID, domain.EventTypeTransferCompleted, body); err == nil {
		t.Fatal("expected notification insert failure")
	}
	_, _ = pool.Exec(ctx, `DROP TRIGGER test_consumer_notification_failure ON notifications`)
	_, _ = pool.Exec(ctx, `DROP FUNCTION test_consumer_notification_failure()`)
	var processedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM processed_events WHERE event_id=$1`, failedEventID).Scan(&processedCount); err != nil || processedCount != 0 {
		t.Fatalf("failed transaction left processed event count=%d err=%v", processedCount, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE id=$1`, failedEventID); err != nil {
		t.Fatal(err)
	}

	concurrentEventID, concurrentPayload := loadConsumerEvent(t, pool, transfer.TransactionID)
	concurrentID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,aggregate_id,aggregate_type,event_type,payload) SELECT $1,aggregate_id,aggregate_type,event_type,payload FROM outbox_events WHERE id=$2`, concurrentID, concurrentEventID); err != nil {
		t.Fatal(err)
	}
	concurrentBody, _ := json.Marshal(concurrentPayload)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	dupes := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			duplicate, err := processor.Process(ctx, concurrentID, domain.EventTypeTransferCompleted, concurrentBody)
			dupes <- duplicate
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(dupes)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent processing: %v", err)
		}
	}
	duplicateCount := 0
	for duplicate := range dupes {
		if duplicate {
			duplicateCount++
		}
	}
	if duplicateCount != 1 {
		t.Fatalf("concurrent duplicate results=%d, want exactly one", duplicateCount)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event_id=$1`, concurrentID).Scan(&notificationCount); err != nil || notificationCount != 2 {
		t.Fatalf("concurrent notifications=%d err=%v", notificationCount, err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM notifications WHERE event_id=$1`, concurrentID)
	_, _ = pool.Exec(ctx, `DELETE FROM processed_events WHERE event_id=$1`, concurrentID)
	_, _ = pool.Exec(ctx, `DELETE FROM outbox_events WHERE id=$1`, concurrentID)
}

func consumerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("CONSUMER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set CONSUMER_TEST_DATABASE_URL to a dedicated PostgreSQL test database with migrations applied")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("CONSUMER_TEST_DATABASE_URL database name must end in _test")
	}
	for _, key := range []string{"TEST_DATABASE_URL", "OUTBOX_TEST_DATABASE_URL"} {
		other := os.Getenv(key)
		if other == "" {
			continue
		}
		otherCfg, e := url.Parse(other)
		if e != nil {
			t.Fatalf("parse %s: %v", key, e)
		}
		if otherCfg.Host == cfg.ConnConfig.Host && strings.TrimPrefix(otherCfg.Path, "/") == cfg.ConnConfig.Database {
			t.Fatalf("CONSUMER_TEST_DATABASE_URL must be separate from %s", key)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	var ready bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.notifications') IS NOT NULL AND to_regclass('public.processed_events') IS NOT NULL`).Scan(&ready); err != nil || !ready {
		pool.Close()
		t.Fatalf("consumer migrations are missing: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func loadConsumerEvent(t *testing.T, pool *pgxpool.Pool, transferID uuid.UUID) (uuid.UUID, domain.TransferCompletedPayload) {
	t.Helper()
	var id uuid.UUID
	var raw []byte
	if err := pool.QueryRow(context.Background(), `SELECT id,payload FROM outbox_events WHERE aggregate_id=$1 AND event_type=$2 ORDER BY created_at DESC LIMIT 1`, transferID, domain.EventTypeTransferCompleted).Scan(&id, &raw); err != nil {
		t.Fatal(err)
	}
	var payload domain.TransferCompletedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return id, payload
}

func cleanupConsumerFixture(t *testing.T, pool *pgxpool.Pool, userIDs ...uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin fixture cleanup: %v", err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `ALTER TABLE ledger_entries DISABLE TRIGGER ledger_entries_append_only`); err != nil {
		t.Errorf("disable ledger cleanup trigger: %v", err)
		return
	}
	for _, id := range userIDs {
		_, _ = tx.Exec(ctx, `DELETE FROM notifications WHERE user_id=$1 OR transfer_id IN (SELECT id FROM ledger_transactions WHERE user_id=$1 OR wallet_id IN (SELECT id FROM wallets WHERE user_id=$1) OR destination_wallet_id IN (SELECT id FROM wallets WHERE user_id=$1))`, id)
		_, _ = tx.Exec(ctx, `DELETE FROM processed_events WHERE event_id IN (SELECT id FROM outbox_events WHERE aggregate_id IN (SELECT id FROM ledger_transactions WHERE user_id=$1 OR wallet_id IN (SELECT id FROM wallets WHERE user_id=$1) OR destination_wallet_id IN (SELECT id FROM wallets WHERE user_id=$1)))`, id)
		_, _ = tx.Exec(ctx, `DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM ledger_transactions WHERE user_id=$1 OR wallet_id IN (SELECT id FROM wallets WHERE user_id=$1) OR destination_wallet_id IN (SELECT id FROM wallets WHERE user_id=$1))`, id)
		_, _ = tx.Exec(ctx, `DELETE FROM ledger_entries WHERE transaction_id IN (SELECT id FROM ledger_transactions WHERE user_id=$1 OR wallet_id IN (SELECT id FROM wallets WHERE user_id=$1) OR destination_wallet_id IN (SELECT id FROM wallets WHERE user_id=$1))`, id)
	}
	for _, id := range userIDs {
		_, _ = tx.Exec(ctx, `DELETE FROM ledger_transactions WHERE user_id=$1 OR wallet_id IN (SELECT id FROM wallets WHERE user_id=$1) OR destination_wallet_id IN (SELECT id FROM wallets WHERE user_id=$1)`, id)
		_, _ = tx.Exec(ctx, `DELETE FROM wallets WHERE user_id=$1`, id)
		_, _ = tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE ledger_entries ENABLE TRIGGER ledger_entries_append_only`); err != nil {
		t.Errorf("enable ledger cleanup trigger: %v", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit fixture cleanup: %v", err)
	}
}
