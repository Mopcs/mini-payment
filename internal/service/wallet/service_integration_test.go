package wallet

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

func TestWalletServiceIntegration(t *testing.T) {
	pool := walletTestPool(t)
	service := NewService(pool)
	user := &domain.User{Email: fmt.Sprintf("wallet-%s@example.test", uuid.NewString()), PasswordHash: "test-only-hash"}
	if err := postgres.NewUserRepository(pool).Create(context.Background(), user); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, `DELETE FROM wallets WHERE user_id = $1`, user.ID); err != nil {
			t.Errorf("delete test wallets: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})

	for _, currency := range []string{" usd ", "eur", "JPY"} {
		wallet, err := service.Create(context.Background(), user.ID, currency)
		if err != nil {
			t.Fatalf("create wallet %s: %v", currency, err)
		}
		if wallet.Currency != strings.ToUpper(strings.TrimSpace(currency)) || wallet.Balance != 0 || wallet.ID == uuid.Nil {
			t.Fatalf("created wallet = %+v", wallet)
		}
	}
	list, err := service.List(context.Background(), user.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("List() = %d wallets, %v", len(list), err)
	}
	if list[0].Currency != "EUR" || list[1].Currency != "JPY" || list[2].Currency != "USD" {
		t.Fatalf("wallet order = %v, %v, %v", list[0].Currency, list[1].Currency, list[2].Currency)
	}
	if _, err := service.Create(context.Background(), user.ID, "CAD"); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("unsupported currency error = %v", err)
	}
	if _, err := service.Create(context.Background(), user.ID, "usd"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate currency error = %v", err)
	}

	otherUser := uuid.New()
	if _, err := service.Get(context.Background(), otherUser, list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign wallet lookup error = %v, want ErrNotFound", err)
	}
	if _, err := service.Get(context.Background(), user.ID, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing wallet lookup error = %v, want ErrNotFound", err)
	}

	concurrentUser := &domain.User{Email: fmt.Sprintf("wallet-race-%s@example.test", uuid.NewString()), PasswordHash: "test-only-hash"}
	if err := postgres.NewUserRepository(pool).Create(context.Background(), concurrentUser); err != nil {
		t.Fatalf("create concurrent user: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, `DELETE FROM wallets WHERE user_id = $1`, concurrentUser.ID); err != nil {
			t.Errorf("delete concurrent wallets: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, concurrentUser.ID); err != nil {
			t.Errorf("delete concurrent user: %v", err)
		}
	})
	const attempts = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, conflicts := 0, 0
	unexpected := make([]error, 0)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Create(context.Background(), concurrentUser.ID, "EUR")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrAlreadyExists):
				conflicts++
			default:
				unexpected = append(unexpected, err)
			}
		}()
	}
	wg.Wait()
	if successes != 1 || conflicts != attempts-1 || len(unexpected) != 0 {
		t.Fatalf("concurrent creates: successes=%d conflicts=%d unexpected=%v", successes, conflicts, unexpected)
	}
}

func TestDepositIntegration(t *testing.T) {
	pool := walletTestPool(t)
	service := NewService(pool)
	user := createWalletIntegrationUser(t, pool, "deposit")
	t.Cleanup(func() { cleanupDepositData(t, pool, user.ID) })
	ownerWallet, err := service.Create(context.Background(), user.ID, "USD")
	if err != nil {
		t.Fatalf("create wallet: %v", err)
	}

	key := "deposit-" + uuid.NewString()
	first, replay, err := service.Deposit(context.Background(), user.ID, ownerWallet.ID, 5000, "usd", key)
	if err != nil || replay {
		t.Fatalf("first deposit = %+v replay=%t err=%v", first, replay, err)
	}
	if first.BalanceAfter != 5000 || first.Amount != 5000 || first.Currency != "USD" {
		t.Fatalf("deposit result = %+v", first)
	}
	second, replay, err := service.Deposit(context.Background(), user.ID, ownerWallet.ID, 5000, "USD", key)
	if err != nil || !replay || second != first {
		t.Fatalf("retry = %+v replay=%t err=%v, want original %+v", second, replay, err, first)
	}
	if _, _, err := service.Deposit(context.Background(), user.ID, ownerWallet.ID, 6000, "USD", key); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed retry error = %v, want ErrIdempotencyConflict", err)
	}
	if _, _, err := service.Deposit(context.Background(), user.ID, ownerWallet.ID, 1, "EUR", "currency-"+uuid.NewString()); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("currency mismatch error = %v, want ErrCurrencyMismatch", err)
	}
	otherUser := createWalletIntegrationUser(t, pool, "deposit-other")
	if _, _, err := service.Deposit(context.Background(), otherUser.ID, ownerWallet.ID, 1, "USD", "owner-"+uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign wallet error = %v, want ErrNotFound", err)
	}
	if _, _, err := service.Deposit(context.Background(), user.ID, ownerWallet.ID, 0, "USD", "amount-"+uuid.NewString()); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("zero amount error = %v, want ErrInvalidAmount", err)
	}

	var entryCount int
	var entryTotal int64
	var walletEntry, fundingEntry int64
	err = pool.QueryRow(context.Background(), `
		SELECT count(*), sum(le.amount),
		       sum(le.amount) FILTER (WHERE la.account_type = 'wallet'),
		       sum(le.amount) FILTER (WHERE la.account_type = 'system_funding')
		FROM ledger_entries le JOIN ledger_accounts la ON la.id = le.account_id
		WHERE le.transaction_id = $1`, first.TransactionID,
	).Scan(&entryCount, &entryTotal, &walletEntry, &fundingEntry)
	if err != nil || entryCount != 2 || entryTotal != 0 || walletEntry != 5000 || fundingEntry != -5000 {
		t.Fatalf("ledger entries count=%d total=%d wallet=%d funding=%d err=%v", entryCount, entryTotal, walletEntry, fundingEntry, err)
	}
	current, err := service.Get(context.Background(), user.ID, ownerWallet.ID)
	if err != nil || current.Balance != 5000 {
		t.Fatalf("wallet after replay = %+v err=%v", current, err)
	}

	var walletAccountID uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT id FROM ledger_accounts WHERE wallet_id = $1 AND account_type = 'wallet'`, ownerWallet.ID).Scan(&walletAccountID); err != nil {
		t.Fatalf("load wallet ledger account: %v", err)
	}
	invalidLedgerTx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin invalid ledger test: %v", err)
	}
	invalidTransactionID := uuid.New()
	if _, err := invalidLedgerTx.Exec(context.Background(), `
		INSERT INTO ledger_transactions (id, transaction_type, user_id, idempotency_key, request_hash, wallet_id, currency, amount, balance_after)
		VALUES ($1, 'deposit', $2, $3, $4, $5, 'USD', 1, 5001)`, invalidTransactionID, user.ID, "invalid-ledger-"+uuid.NewString(), make([]byte, 32), ownerWallet.ID); err != nil {
		_ = invalidLedgerTx.Rollback(context.Background())
		t.Fatalf("insert invalid ledger transaction: %v", err)
	}
	if _, err := invalidLedgerTx.Exec(context.Background(), `INSERT INTO ledger_entries (transaction_id, account_id, amount) VALUES ($1, $2, 1)`, invalidTransactionID, walletAccountID); err != nil {
		_ = invalidLedgerTx.Rollback(context.Background())
		t.Fatalf("insert intentionally unbalanced ledger entry: %v", err)
	}
	if err := invalidLedgerTx.Commit(context.Background()); err == nil {
		t.Fatal("database accepted an unbalanced ledger transaction")
	}

	var ledgerEntryID uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT id FROM ledger_entries WHERE transaction_id = $1 LIMIT 1`, first.TransactionID).Scan(&ledgerEntryID); err != nil {
		t.Fatalf("load ledger entry: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE ledger_entries SET amount = amount + 1 WHERE id = $1`, ledgerEntryID); err == nil {
		t.Fatal("ledger entry update unexpectedly succeeded")
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM ledger_entries WHERE id = $1`, ledgerEntryID); err == nil {
		t.Fatal("ledger entry delete unexpectedly succeeded")
	}

	missingAccountWallet, err := service.Create(context.Background(), user.ID, "EUR")
	if err != nil {
		t.Fatalf("create wallet for missing-account case: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM ledger_accounts WHERE wallet_id = $1 AND account_type = 'wallet'`, missingAccountWallet.ID); err != nil {
		t.Fatalf("remove wallet ledger account for test: %v", err)
	}
	if _, _, err := service.Deposit(context.Background(), user.ID, missingAccountWallet.ID, 100, "EUR", "no-account-"+uuid.NewString()); !errors.Is(err, ErrLedgerAccountMissing) {
		t.Fatalf("missing ledger account error = %v, want ErrLedgerAccountMissing", err)
	}
	missingAfter, err := service.Get(context.Background(), user.ID, missingAccountWallet.ID)
	if err != nil || missingAfter.Balance != 0 {
		t.Fatalf("missing account changed balance: %+v err=%v", missingAfter, err)
	}

	rollbackWallet, err := service.Create(context.Background(), user.ID, "JPY")
	if err != nil {
		t.Fatalf("create rollback wallet: %v", err)
	}
	installLedgerFailure(t, pool, rollbackWallet.ID)
	if _, _, err := service.Deposit(context.Background(), user.ID, rollbackWallet.ID, 100, "JPY", "rollback-"+uuid.NewString()); err == nil {
		t.Fatal("deposit unexpectedly succeeded with failing ledger trigger")
	}
	removeLedgerFailure(t, pool, rollbackWallet.ID)
	rollbackAfter, err := service.Get(context.Background(), user.ID, rollbackWallet.ID)
	if err != nil || rollbackAfter.Balance != 0 {
		t.Fatalf("failed deposit changed wallet: %+v err=%v", rollbackAfter, err)
	}

	parallelUser := createWalletIntegrationUser(t, pool, "deposit-parallel")
	t.Cleanup(func() { cleanupDepositData(t, pool, parallelUser.ID) })
	concurrentWallet, err := service.Create(context.Background(), parallelUser.ID, "JPY")
	if err != nil {
		t.Fatalf("create concurrent wallet: %v", err)
	}
	const concurrent = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]domain.Deposit, 0, concurrent)
	unexpected := make([]error, 0)
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			item, _, err := service.Deposit(context.Background(), parallelUser.ID, concurrentWallet.ID, 100, "JPY", fmt.Sprintf("parallel-%s-%d", uuid.NewString(), i))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unexpected = append(unexpected, err)
			} else {
				results = append(results, item)
			}
		}(i)
	}
	wg.Wait()
	if len(results) != concurrent || len(unexpected) != 0 {
		t.Fatalf("concurrent deposits: results=%d errors=%v", len(results), unexpected)
	}
	if got, err := service.Get(context.Background(), parallelUser.ID, concurrentWallet.ID); err != nil || got.Balance != 100*concurrent {
		t.Fatalf("balance after concurrent deposits = %+v err=%v", got, err)
	}

	idempotentWallet, err := service.Create(context.Background(), parallelUser.ID, "EUR")
	if err != nil {
		t.Fatalf("create concurrent idempotency wallet: %v", err)
	}
	idempotencyKey := "parallel-same-" + uuid.NewString()
	results = results[:0]
	unexpected = unexpected[:0]
	replayed := 0
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, replay, err := service.Deposit(context.Background(), parallelUser.ID, idempotentWallet.ID, 250, "EUR", idempotencyKey)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unexpected = append(unexpected, err)
			} else {
				results = append(results, item)
				if replay {
					replayed++
				}
			}
		}()
	}
	wg.Wait()
	if len(results) != concurrent || len(unexpected) != 0 || replayed != concurrent-1 {
		t.Fatalf("same-key requests: results=%d replays=%d errors=%v", len(results), replayed, unexpected)
	}
	for _, item := range results {
		if item.TransactionID != results[0].TransactionID || item.BalanceAfter != 250 {
			t.Fatalf("same-key result differs: %+v / %+v", item, results[0])
		}
	}
	if got, err := service.Get(context.Background(), parallelUser.ID, idempotentWallet.ID); err != nil || got.Balance != 250 {
		t.Fatalf("balance after duplicate requests = %+v err=%v", got, err)
	}
	if _, _, err := service.Deposit(context.Background(), parallelUser.ID, idempotentWallet.ID, math.MaxInt64, "EUR", "overflow-"+uuid.NewString()); !errors.Is(err, ErrBalanceLimit) {
		t.Fatalf("overflow error = %v, want ErrBalanceLimit", err)
	}
}

func createWalletIntegrationUser(t *testing.T, pool *pgxpool.Pool, prefix string) *domain.User {
	t.Helper()
	user := &domain.User{Email: fmt.Sprintf("%s-%s@example.test", prefix, uuid.NewString()), PasswordHash: "test-only-hash"}
	if err := postgres.NewUserRepository(pool).Create(context.Background(), user); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, `DELETE FROM wallets WHERE user_id = $1`, user.ID); err != nil {
			t.Errorf("delete test wallets: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	return user
}

func installLedgerFailure(t *testing.T, pool *pgxpool.Pool, walletID uuid.UUID) {
	t.Helper()
	name := "test_fail_ledger_" + strings.ReplaceAll(walletID.String(), "-", "")
	function := "CREATE FUNCTION " + name + `() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'test ledger failure'; END; $$ LANGUAGE plpgsql`
	if _, err := pool.Exec(context.Background(), function); err != nil {
		t.Fatalf("create failure function: %v", err)
	}
	var accountID uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT id FROM ledger_accounts WHERE wallet_id = $1`, walletID).Scan(&accountID); err != nil {
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION "+name+"()")
		t.Fatalf("load wallet ledger account: %v", err)
	}
	trigger := "test_" + name
	statement := fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON ledger_entries FOR EACH ROW WHEN (NEW.account_id = '%s'::uuid) EXECUTE FUNCTION %s()", trigger, accountID, name)
	if _, err := pool.Exec(context.Background(), statement); err != nil {
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION "+name+"()")
		t.Fatalf("create failure trigger: %v", err)
	}
	t.Cleanup(func() { removeLedgerFailure(t, pool, walletID) })
}

func removeLedgerFailure(t *testing.T, pool *pgxpool.Pool, walletID uuid.UUID) {
	t.Helper()
	name := "test_fail_ledger_" + strings.ReplaceAll(walletID.String(), "-", "")
	trigger := "test_" + name
	if _, err := pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+trigger+" ON ledger_entries"); err != nil {
		t.Errorf("drop failure trigger: %v", err)
	}
	if _, err := pool.Exec(context.Background(), "DROP FUNCTION IF EXISTS "+name+"()"); err != nil {
		t.Errorf("drop failure function: %v", err)
	}
}

func cleanupDepositData(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Errorf("begin deposit test cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `ALTER TABLE ledger_entries DISABLE TRIGGER ledger_entries_append_only`); err != nil {
		t.Errorf("disable append-only trigger for isolated test cleanup: %v", err)
		return
	}
	if _, err := tx.Exec(context.Background(), `DELETE FROM ledger_entries WHERE transaction_id IN (SELECT id FROM ledger_transactions WHERE user_id = $1)`, userID); err != nil {
		t.Errorf("delete test ledger entries: %v", err)
		return
	}
	if _, err := tx.Exec(context.Background(), `DELETE FROM ledger_transactions WHERE user_id = $1`, userID); err != nil {
		t.Errorf("delete test ledger transactions: %v", err)
		return
	}
	if _, err := tx.Exec(context.Background(), `ALTER TABLE ledger_entries ENABLE TRIGGER ledger_entries_append_only`); err != nil {
		t.Errorf("re-enable append-only trigger: %v", err)
		return
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Errorf("commit deposit test cleanup: %v", err)
	}
}

func walletTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated PostgreSQL test database with migrations applied")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("TEST_DATABASE_URL must point to a dedicated database whose name ends in _test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test database: %v", err)
	}
	var schemaReady bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.users') IS NOT NULL AND to_regclass('public.wallets') IS NOT NULL AND to_regclass('public.ledger_accounts') IS NOT NULL AND to_regclass('public.ledger_transactions') IS NOT NULL AND to_regclass('public.ledger_entries') IS NOT NULL`).Scan(&schemaReady); err != nil || !schemaReady {
		pool.Close()
		if err != nil {
			t.Fatalf("check test schema: %v", err)
		}
		t.Fatal("test database schema is missing; apply migrations before running integration tests")
	}
	t.Cleanup(pool.Close)
	return pool
}
