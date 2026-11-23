package wallet

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
)

func TestTransferIntegration(t *testing.T) {
	pool := walletTestPool(t)
	wallets := NewService(pool)
	transfers := transferservice.NewService(pool)
	sourceUser := createWalletIntegrationUser(t, pool, "transfer-source")
	destinationUser := createWalletIntegrationUser(t, pool, "transfer-destination")
	t.Cleanup(func() { cleanupDepositData(t, pool, sourceUser.ID) })
	t.Cleanup(func() { cleanupDepositData(t, pool, destinationUser.ID) })

	sourceWallet, err := wallets.Create(context.Background(), sourceUser.ID, "USD")
	if err != nil {
		t.Fatalf("create source wallet: %v", err)
	}
	destinationWallet, err := wallets.Create(context.Background(), destinationUser.ID, "USD")
	if err != nil {
		t.Fatalf("create destination wallet: %v", err)
	}
	if _, _, err := wallets.Deposit(context.Background(), sourceUser.ID, sourceWallet.ID, 10_000, "USD", "fund-"+uuid.NewString()); err != nil {
		t.Fatalf("fund source wallet: %v", err)
	}

	key := "transfer-" + uuid.NewString()
	first, replay, err := transfers.Transfer(context.Background(), sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 2500, " usd ", key)
	if err != nil || replay {
		t.Fatalf("first transfer = %+v replay=%t err=%v", first, replay, err)
	}
	if first.SourceBalanceAfter != 7500 || first.DestinationBalanceAfter != 2500 || first.Amount != 2500 || first.Currency != "USD" {
		t.Fatalf("transfer result = %+v", first)
	}
	second, replay, err := transfers.Transfer(context.Background(), sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 2500, "USD", key)
	if err != nil || !replay || second != first {
		t.Fatalf("idempotent retry = %+v replay=%t err=%v, want %+v", second, replay, err, first)
	}
	if _, _, err := transfers.Transfer(context.Background(), sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 2501, "USD", key); !errors.Is(err, transferservice.ErrIdempotencyConflict) {
		t.Fatalf("changed retry error = %v, want idempotency conflict", err)
	}

	var entryCount int
	var entryTotal, sourceEntry, destinationEntry int64
	err = pool.QueryRow(context.Background(), `
		SELECT count(*), sum(le.amount),
		       sum(le.amount) FILTER (WHERE la.wallet_id = $2),
		       sum(le.amount) FILTER (WHERE la.wallet_id = $3)
		FROM ledger_entries le JOIN ledger_accounts la ON la.id = le.account_id
		WHERE le.transaction_id = $1`, first.TransactionID, sourceWallet.ID, destinationWallet.ID,
	).Scan(&entryCount, &entryTotal, &sourceEntry, &destinationEntry)
	if err != nil || entryCount != 2 || entryTotal != 0 || sourceEntry != -2500 || destinationEntry != 2500 {
		t.Fatalf("transfer ledger count=%d total=%d source=%d destination=%d err=%v", entryCount, entryTotal, sourceEntry, destinationEntry, err)
	}
	assertWalletBalances(t, wallets, sourceUser.ID, sourceWallet.ID, destinationUser.ID, destinationWallet.ID, 7500, 2500)

	if _, _, err := transfers.Transfer(context.Background(), sourceUser.ID, sourceWallet.ID, sourceWallet.ID, 1, "USD", "same-"+uuid.NewString()); !errors.Is(err, transferservice.ErrSameWallet) {
		t.Fatalf("same-wallet transfer error = %v", err)
	}
	insufficientKey := "insufficient-" + uuid.NewString()
	if _, _, err := transfers.Transfer(context.Background(), sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 8000, "USD", insufficientKey); !errors.Is(err, transferservice.ErrInsufficientFunds) {
		t.Fatalf("insufficient funds error = %v", err)
	}
	if _, _, err := transfers.Transfer(context.Background(), destinationUser.ID, sourceWallet.ID, destinationWallet.ID, 1, "USD", "not-owner-"+uuid.NewString()); !errors.Is(err, transferservice.ErrNotFound) {
		t.Fatalf("source ownership error = %v", err)
	}
	if _, _, err := transfers.Transfer(context.Background(), sourceUser.ID, sourceWallet.ID, uuid.New(), 1, "USD", "missing-"+uuid.NewString()); !errors.Is(err, transferservice.ErrNotFound) {
		t.Fatalf("missing wallet error = %v", err)
	}
	euroWallet, err := wallets.Create(context.Background(), destinationUser.ID, "EUR")
	if err != nil {
		t.Fatalf("create EUR wallet: %v", err)
	}
	if _, _, err := transfers.Transfer(context.Background(), sourceUser.ID, sourceWallet.ID, euroWallet.ID, 1, "USD", "currency-"+uuid.NewString()); !errors.Is(err, transferservice.ErrCurrencyMismatch) {
		t.Fatalf("currency mismatch error = %v", err)
	}
	var failedKeyCount int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_transactions WHERE user_id = $1 AND idempotency_key = $2`, sourceUser.ID, insufficientKey).Scan(&failedKeyCount); err != nil || failedKeyCount != 0 {
		t.Fatalf("failed transfer left idempotency state: count=%d err=%v", failedKeyCount, err)
	}

	if _, err := pool.Exec(context.Background(), `UPDATE wallets SET balance = 9223372036854775807 WHERE id = $1`, destinationWallet.ID); err != nil {
		t.Fatalf("set destination overflow fixture: %v", err)
	}
	if _, _, err := transfers.Transfer(context.Background(), sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 1, "USD", "overflow-"+uuid.NewString()); !errors.Is(err, transferservice.ErrBalanceLimit) {
		t.Fatalf("destination overflow error = %v", err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE wallets SET balance = 2500 WHERE id = $1`, destinationWallet.ID); err != nil {
		t.Fatalf("restore destination balance fixture: %v", err)
	}

	rollbackKey := "rollback-" + uuid.NewString()
	installLedgerFailure(t, pool, destinationWallet.ID)
	if _, _, err := transfers.Transfer(context.Background(), sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 100, "USD", rollbackKey); err == nil {
		t.Fatal("transfer unexpectedly succeeded with failing destination ledger trigger")
	}
	removeLedgerFailure(t, pool, destinationWallet.ID)
	assertWalletBalances(t, wallets, sourceUser.ID, sourceWallet.ID, destinationUser.ID, destinationWallet.ID, 7500, 2500)
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_transactions WHERE user_id = $1 AND idempotency_key = $2`, sourceUser.ID, rollbackKey).Scan(&failedKeyCount); err != nil || failedKeyCount != 0 {
		t.Fatalf("rolled-back transfer retained idempotency row: count=%d err=%v", failedKeyCount, err)
	}

	runOppositeDirectionTransfers(t, transfers, sourceUser.ID, destinationUser.ID, sourceWallet.ID, destinationWallet.ID)
	assertWalletBalances(t, wallets, sourceUser.ID, sourceWallet.ID, destinationUser.ID, destinationWallet.ID, 7500, 2500)

	const parallelRequests = 8
	parallelKey := "same-key-" + uuid.NewString()
	parallelResults := make([]domain.Transfer, 0, parallelRequests)
	parallelErrors := make([]error, 0)
	replays := 0
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < parallelRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			result, replay, err := transfers.Transfer(ctx, sourceUser.ID, sourceWallet.ID, destinationWallet.ID, 100, "USD", parallelKey)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				parallelErrors = append(parallelErrors, err)
				return
			}
			parallelResults = append(parallelResults, result)
			if replay {
				replays++
			}
		}()
	}
	wg.Wait()
	if len(parallelResults) != parallelRequests || len(parallelErrors) != 0 || replays != parallelRequests-1 {
		t.Fatalf("duplicate requests: results=%d replays=%d errors=%v", len(parallelResults), replays, parallelErrors)
	}
	for _, result := range parallelResults {
		if result.TransactionID != parallelResults[0].TransactionID || result.SourceBalanceAfter != 7400 || result.DestinationBalanceAfter != 2600 {
			t.Fatalf("duplicate result differs: %+v / %+v", result, parallelResults[0])
		}
	}

	runConcurrentSourceTransfers(t, transfers, wallets, sourceUser.ID, destinationUser.ID, sourceWallet.ID, destinationWallet.ID, 7400, 10_000)
	var invalidTransferCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM (
			SELECT t.id FROM ledger_transactions t
			JOIN ledger_entries le ON le.transaction_id = t.id
			WHERE t.transaction_type = 'transfer' AND t.user_id = $1
			GROUP BY t.id HAVING count(*) <> 2 OR sum(le.amount) <> 0
		) invalid`, sourceUser.ID,
	).Scan(&invalidTransferCount); err != nil || invalidTransferCount != 0 {
		t.Fatalf("invalid transfer ledger rows=%d err=%v", invalidTransferCount, err)
	}
}

func runOppositeDirectionTransfers(t *testing.T, service *transferservice.Service, sourceUserID, destinationUserID, sourceWalletID, destinationWalletID uuid.UUID) {
	t.Helper()
	const count = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	for i := 0; i < count*2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			userID, from, to := sourceUserID, sourceWalletID, destinationWalletID
			if i%2 == 1 {
				userID, from, to = destinationUserID, destinationWalletID, sourceWalletID
			}
			_, _, err := service.Transfer(ctx, userID, from, to, 1, "USD", fmt.Sprintf("opposite-%s-%d", uuid.NewString(), i))
			if err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(failures) != 0 {
		t.Fatalf("opposite direction transfers failed or deadlocked: %v", failures)
	}
}

func runConcurrentSourceTransfers(t *testing.T, service *transferservice.Service, wallets *Service, sourceUserID, destinationUserID, sourceWalletID, destinationWalletID uuid.UUID, startingSource, totalBalance int64) {
	t.Helper()
	const attempts = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, insufficient := 0, 0
	var unexpected []error
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, _, err := service.Transfer(ctx, sourceUserID, sourceWalletID, destinationWalletID, 1000, "USD", fmt.Sprintf("overspend-%s-%d", uuid.NewString(), i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, transferservice.ErrInsufficientFunds):
				insufficient++
			default:
				unexpected = append(unexpected, err)
			}
		}(i)
	}
	wg.Wait()
	wantSuccesses := int(startingSource / 1000)
	if successes != wantSuccesses || insufficient != attempts-wantSuccesses || len(unexpected) != 0 {
		t.Fatalf("concurrent source transfers: succeeded=%d insufficient=%d unexpected=%v", successes, insufficient, unexpected)
	}
	source, err := wallets.Get(context.Background(), sourceUserID, sourceWalletID)
	if err != nil || source.Balance != startingSource-int64(successes)*1000 || source.Balance < 0 {
		t.Fatalf("source balance after overspend attempts = %+v err=%v", source, err)
	}
	destination, err := wallets.Get(context.Background(), destinationUserID, destinationWalletID)
	if err != nil || source.Balance+destination.Balance != totalBalance {
		t.Fatalf("wallet balance was not conserved: source=%+v destination=%+v err=%v", source, destination, err)
	}
}

func assertWalletBalances(t *testing.T, wallets *Service, sourceUserID, sourceWalletID, destinationUserID, destinationWalletID uuid.UUID, sourceWant, destinationWant int64) {
	t.Helper()
	source, err := wallets.Get(context.Background(), sourceUserID, sourceWalletID)
	if err != nil || source.Balance != sourceWant {
		t.Fatalf("source wallet = %+v err=%v, want balance %d", source, err, sourceWant)
	}
	destination, err := wallets.Get(context.Background(), destinationUserID, destinationWalletID)
	if err != nil || destination.Balance != destinationWant {
		t.Fatalf("destination wallet = %+v err=%v, want balance %d", destination, err, destinationWant)
	}
}
