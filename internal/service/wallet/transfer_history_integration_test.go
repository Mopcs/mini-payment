package wallet

import (
	"context"
	"testing"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
	"github.com/google/uuid"
)

func TestTransferHistoryIntegration(t *testing.T) {
	pool := walletTestPool(t)
	wallets := NewService(pool)
	transfers := transferservice.NewService(pool)
	userA := createWalletIntegrationUser(t, pool, "history-a")
	userB := createWalletIntegrationUser(t, pool, "history-b")
	userC := createWalletIntegrationUser(t, pool, "history-c")
	for _, userID := range []uuid.UUID{userA.ID, userB.ID, userC.ID} {
		t.Cleanup(func() { cleanupDepositData(t, pool, userID) })
	}

	createWallet := func(userID uuid.UUID, currency string) uuid.UUID {
		t.Helper()
		wallet, err := wallets.Create(context.Background(), userID, currency)
		if err != nil {
			t.Fatalf("create %s wallet: %v", currency, err)
		}
		return wallet.ID
	}
	aUSD, aEUR := createWallet(userA.ID, "USD"), createWallet(userA.ID, "EUR")
	bUSD, bEUR := createWallet(userB.ID, "USD"), createWallet(userB.ID, "EUR")
	cUSD := createWallet(userC.ID, "USD")
	for _, funding := range []struct {
		owner, wallet uuid.UUID
		currency      string
	}{
		{userA.ID, aUSD, "USD"}, {userA.ID, aEUR, "EUR"},
		{userB.ID, bUSD, "USD"}, {userB.ID, bEUR, "EUR"}, {userC.ID, cUSD, "USD"},
	} {
		if _, _, err := wallets.Deposit(context.Background(), funding.owner, funding.wallet, 5000, funding.currency, "fund-"+uuid.NewString()); err != nil {
			t.Fatalf("fund wallet: %v", err)
		}
	}
	makeTransfer := func(owner, source, destination uuid.UUID, amount int64) uuid.UUID {
		t.Helper()
		result, _, err := transfers.Transfer(context.Background(), owner, source, destination, amount, "USD", "history-"+uuid.NewString())
		if err != nil {
			t.Fatalf("create transfer: %v", err)
		}
		return result.TransactionID
	}
	// A has no transfers before any fixtures are created.
	items, total, err := transfers.List(context.Background(), userA.ID, historyFilter(20, 0, "", ""))
	if err != nil || len(items) != 0 || total != 0 {
		t.Fatalf("empty history: items=%v total=%d err=%v", items, total, err)
	}

	outgoing1 := makeTransfer(userA.ID, aUSD, bUSD, 100)
	incoming1 := makeTransfer(userB.ID, bUSD, aUSD, 200)
	outgoing2 := makeTransfer(userA.ID, aUSD, bUSD, 300)
	unrelated := makeTransfer(userC.ID, cUSD, bUSD, 400)
	_ = unrelated
	if _, _, err := transfers.Transfer(context.Background(), userB.ID, bEUR, aEUR, 75, "EUR", "history-eur-"+uuid.NewString()); err != nil {
		t.Fatalf("create EUR incoming transfer: %v", err)
	}

	// Equal timestamps exercise the UUID tie-breaker independently of insert order.
	equalTime := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	ids := []uuid.UUID{outgoing1, incoming1, outgoing2}
	if _, err := pool.Exec(context.Background(), `UPDATE ledger_transactions SET created_at = $1 WHERE id = ANY($2)`, equalTime, ids); err != nil {
		t.Fatalf("set equal transfer timestamps: %v", err)
	}

	items, total, err = transfers.List(context.Background(), userA.ID, historyFilter(20, 0, "", ""))
	if err != nil || total != 4 || len(items) != 4 {
		t.Fatalf("mixed history: len=%d total=%d err=%v", len(items), total, err)
	}
	for i := 2; i < 4; i++ {
		if items[i-1].ID.String() < items[i].ID.String() {
			t.Fatalf("equal timestamp order is not descending: %v", items)
		}
	}
	for _, item := range items {
		if item.Status != "completed" {
			t.Fatalf("status=%q", item.Status)
		}
		if item.ID == unrelated {
			t.Fatal("history exposed another user's transfer")
		}
	}

	outgoing, outgoingTotal, err := transfers.List(context.Background(), userA.ID, historyFilter(20, 0, "outgoing", ""))
	if err != nil || outgoingTotal != 2 || len(outgoing) != 2 {
		t.Fatalf("outgoing: len=%d total=%d err=%v", len(outgoing), outgoingTotal, err)
	}
	incoming, incomingTotal, err := transfers.List(context.Background(), userA.ID, historyFilter(20, 0, "incoming", ""))
	if err != nil || incomingTotal != 2 || len(incoming) != 2 {
		t.Fatalf("incoming: len=%d total=%d err=%v", len(incoming), incomingTotal, err)
	}
	euro, euroTotal, err := transfers.List(context.Background(), userA.ID, historyFilter(20, 0, "", "EUR"))
	if err != nil || euroTotal != 1 || len(euro) != 1 || euro[0].Currency != "EUR" {
		t.Fatalf("EUR filter: items=%v total=%d err=%v", euro, euroTotal, err)
	}

	page1, page1Total, err := transfers.List(context.Background(), userA.ID, historyFilter(2, 0, "", ""))
	if err != nil || page1Total != 4 || len(page1) != 2 {
		t.Fatalf("page 1: len=%d total=%d err=%v", len(page1), page1Total, err)
	}
	page2, page2Total, err := transfers.List(context.Background(), userA.ID, historyFilter(2, 2, "", ""))
	if err != nil || page2Total != 4 || len(page2) != 2 || page1[1].ID == page2[0].ID {
		t.Fatalf("page 2: len=%d total=%d err=%v", len(page2), page2Total, err)
	}
	page3, page3Total, err := transfers.List(context.Background(), userA.ID, historyFilter(2, 4, "", ""))
	if err != nil || page3Total != 4 || len(page3) != 0 {
		t.Fatalf("page 3: len=%d total=%d err=%v", len(page3), page3Total, err)
	}

	_, bTotal, err := transfers.List(context.Background(), userB.ID, historyFilter(20, 0, "", ""))
	if err != nil || bTotal != 5 {
		t.Fatalf("user B history total=%d err=%v", bTotal, err)
	}
}

func historyFilter(limit, offset int, direction, currency string) domain.TransferHistoryFilter {
	return domain.TransferHistoryFilter{Limit: limit, Offset: offset, Direction: direction, Currency: currency}
}
