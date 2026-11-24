package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
)

func TestTransferOutboxIntegration(t *testing.T) {
	ctx := context.Background()
	pool := walletTestPool(t)
	wallets := NewService(pool)
	transfers := transferservice.NewService(pool)
	sourceUser := createWalletIntegrationUser(t, pool, "outbox-source")
	destinationUser := createWalletIntegrationUser(t, pool, "outbox-destination")
	otherUser := createWalletIntegrationUser(t, pool, "outbox-other")
	for _, userID := range []uuid.UUID{sourceUser.ID, destinationUser.ID, otherUser.ID} {
		id := userID
		t.Cleanup(func() { cleanupDepositData(t, pool, id) })
	}
	sourceWallet, err := wallets.Create(ctx, sourceUser.ID, "USD")
	if err != nil {
		t.Fatalf("create source wallet: %v", err)
	}
	destinationWallet, err := wallets.Create(ctx, destinationUser.ID, "USD")
	if err != nil {
		t.Fatalf("create destination wallet: %v", err)
	}
	if _, _, err := wallets.Deposit(ctx, sourceUser.ID, sourceWallet.ID, 5000, "USD", "outbox-fund-"+uuid.NewString()); err != nil {
		t.Fatalf("fund source wallet: %v", err)
	}

	firstKey := "outbox-first-" + uuid.NewString()
	first, replay, err := transfers.Transfer(ctx, sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 500, "USD", firstKey)
	if err != nil || replay {
		t.Fatalf("first transfer = %+v replay=%t err=%v", first, replay, err)
	}
	firstEvent := loadTransferOutboxEvent(t, pool, first.TransactionID)
	assertTransferOutboxPayload(t, firstEvent, first, sourceWallet.ID, destinationWallet.ID, 500, "USD")
	if countTransferOutboxEvents(t, pool, first.TransactionID) != 1 {
		t.Fatal("successful transfer did not create exactly one outbox event")
	}

	if _, replay, err := transfers.Transfer(ctx, sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 500, "USD", firstKey); err != nil || !replay {
		t.Fatalf("idempotent replay replay=%t err=%v", replay, err)
	}
	if countTransferOutboxEvents(t, pool, first.TransactionID) != 1 {
		t.Fatal("idempotent replay created another outbox event")
	}

	second, replay, err := transfers.Transfer(ctx, sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 250, "USD", "outbox-second-"+uuid.NewString())
	if err != nil || replay {
		t.Fatalf("second independent transfer = %+v replay=%t err=%v", second, replay, err)
	}
	secondEvent := loadTransferOutboxEvent(t, pool, second.TransactionID)
	assertTransferOutboxPayload(t, secondEvent, second, sourceWallet.ID, destinationWallet.ID, 250, "USD")
	if firstEvent.ID == secondEvent.ID || countTransferOutboxEvents(t, pool, first.TransactionID) != 1 || countTransferOutboxEvents(t, pool, second.TransactionID) != 1 {
		t.Fatal("independent transfers did not create independent outbox events")
	}

	if _, _, err := transfers.Transfer(ctx, otherUser.ID, sourceWallet.ID, destinationWallet.ID, 100, "USD", "outbox-failed-"+uuid.NewString()); !errors.Is(err, transferservice.ErrNotFound) {
		t.Fatalf("unauthorized transfer error = %v, want ErrNotFound", err)
	}
	if _, _, err := transfers.Transfer(ctx, sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 10000, "USD", "outbox-insufficient-"+uuid.NewString()); !errors.Is(err, transferservice.ErrInsufficientFunds) {
		t.Fatalf("insufficient transfer error = %v, want ErrInsufficientFunds", err)
	}
	if countAllTransferOutboxEvents(t, pool, sourceUser.ID) != 2 {
		t.Fatal("a failed transfer created an outbox event")
	}

	atomicKey := "outbox-atomic-" + uuid.NewString()
	installOutboxFailure(t, pool, sourceWallet.ID)
	sourceBefore := integrationWalletBalance(t, pool, sourceWallet.ID)
	destinationBefore := integrationWalletBalance(t, pool, destinationWallet.ID)
	if _, _, err := transfers.Transfer(ctx, sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 300, "USD", atomicKey); err == nil {
		t.Fatal("transfer unexpectedly succeeded while outbox insertion was failing")
	}
	if got := integrationWalletBalance(t, pool, sourceWallet.ID); got != sourceBefore {
		t.Fatalf("source balance after rollback = %d, want %d", got, sourceBefore)
	}
	if got := integrationWalletBalance(t, pool, destinationWallet.ID); got != destinationBefore {
		t.Fatalf("destination balance after rollback = %d, want %d", got, destinationBefore)
	}
	var transactionCount, ledgerEntryCount, idempotencyCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions WHERE user_id = $1 AND idempotency_key = $2`, sourceUser.ID, atomicKey).Scan(&transactionCount); err != nil {
		t.Fatalf("count rolled-back transfer: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries le JOIN ledger_transactions lt ON lt.id = le.transaction_id WHERE lt.user_id = $1 AND lt.idempotency_key = $2`, sourceUser.ID, atomicKey).Scan(&ledgerEntryCount); err != nil {
		t.Fatalf("count rolled-back ledger entries: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions WHERE user_id = $1 AND idempotency_key = $2`, sourceUser.ID, atomicKey).Scan(&idempotencyCount); err != nil {
		t.Fatalf("count rolled-back idempotency result: %v", err)
	}
	if transactionCount != 0 || ledgerEntryCount != 0 || idempotencyCount != 0 || countAllTransferOutboxEvents(t, pool, sourceUser.ID) != 2 {
		t.Fatalf("failed outbox insert left state behind: transactions=%d entries=%d idempotency=%d", transactionCount, ledgerEntryCount, idempotencyCount)
	}

	removeOutboxFailure(t, pool, sourceWallet.ID)
	retried, replay, err := transfers.Transfer(ctx, sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 300, "USD", atomicKey)
	if err != nil || replay {
		t.Fatalf("transfer retry after rollback = %+v replay=%t err=%v", retried, replay, err)
	}
	if countTransferOutboxEvents(t, pool, retried.TransactionID) != 1 {
		t.Fatal("successful retry did not create exactly one outbox event")
	}
}

func loadTransferOutboxEvent(t *testing.T, pool *pgxpool.Pool, transferID uuid.UUID) domain.OutboxEvent {
	t.Helper()
	var event domain.OutboxEvent
	var payload []byte
	err := pool.QueryRow(context.Background(), `
		SELECT id, aggregate_id, aggregate_type, event_type, payload
		FROM outbox_events WHERE aggregate_id = $1`, transferID,
	).Scan(&event.ID, &event.AggregateID, &event.AggregateType, &event.EventType, &payload)
	if err != nil {
		t.Fatalf("load transfer outbox event: %v", err)
	}
	if err := json.Unmarshal(payload, &event.Payload); err != nil {
		t.Fatalf("decode transfer outbox payload: %v", err)
	}
	return event
}

func assertTransferOutboxPayload(t *testing.T, event domain.OutboxEvent, transfer domain.Transfer, sourceID, destinationID uuid.UUID, amount int64, currency string) {
	t.Helper()
	if event.AggregateID != transfer.TransactionID || event.AggregateType != "transfer" || event.EventType != domain.EventTypeTransferCompleted {
		t.Fatalf("outbox metadata = %+v", event)
	}
	payload := event.Payload
	if payload.TransferID != transfer.TransactionID || payload.SourceWalletID != sourceID || payload.DestinationWalletID != destinationID || payload.Amount != amount || payload.Currency != currency || !payload.CompletedAt.Equal(transfer.CreatedAt) {
		t.Fatalf("outbox payload = %+v, transfer=%+v", payload, transfer)
	}
}

func countTransferOutboxEvents(t *testing.T, pool *pgxpool.Pool, transferID uuid.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND event_type = $2`, transferID, domain.EventTypeTransferCompleted).Scan(&count); err != nil {
		t.Fatalf("count transfer outbox events: %v", err)
	}
	return count
}

func countAllTransferOutboxEvents(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM outbox_events e
		JOIN ledger_transactions lt ON lt.id = e.aggregate_id
		WHERE e.event_type = $1 AND lt.user_id = $2`, domain.EventTypeTransferCompleted, userID).Scan(&count); err != nil {
		t.Fatalf("count user's transfer outbox events: %v", err)
	}
	return count
}

func integrationWalletBalance(t *testing.T, pool *pgxpool.Pool, walletID uuid.UUID) int64 {
	t.Helper()
	var balance int64
	if err := pool.QueryRow(context.Background(), `SELECT balance FROM wallets WHERE id = $1`, walletID).Scan(&balance); err != nil {
		t.Fatalf("load wallet balance: %v", err)
	}
	return balance
}

func installOutboxFailure(t *testing.T, pool *pgxpool.Pool, suffix uuid.UUID) {
	t.Helper()
	functionName := "test_fail_outbox_" + strings.ReplaceAll(suffix.String(), "-", "")
	triggerName := "test_" + functionName
	functionSQL := fmt.Sprintf("CREATE FUNCTION %s() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'test outbox failure'; END; $$ LANGUAGE plpgsql", functionName)
	if _, err := pool.Exec(context.Background(), functionSQL); err != nil {
		t.Fatalf("create outbox failure function: %v", err)
	}
	if _, err := pool.Exec(context.Background(), fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON outbox_events FOR EACH ROW WHEN (NEW.payload->>'source_wallet_id' = '%s') EXECUTE FUNCTION %s()", triggerName, suffix, functionName)); err != nil {
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION "+functionName+"()")
		t.Fatalf("create outbox failure trigger: %v", err)
	}
	t.Cleanup(func() { removeOutboxFailure(t, pool, suffix) })
}

func removeOutboxFailure(t *testing.T, pool *pgxpool.Pool, suffix uuid.UUID) {
	t.Helper()
	functionName := "test_fail_outbox_" + strings.ReplaceAll(suffix.String(), "-", "")
	triggerName := "test_" + functionName
	if _, err := pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+triggerName+" ON outbox_events"); err != nil {
		t.Errorf("drop outbox failure trigger: %v", err)
	}
	if _, err := pool.Exec(context.Background(), "DROP FUNCTION IF EXISTS "+functionName+"()"); err != nil {
		t.Errorf("drop outbox failure function: %v", err)
	}
}
