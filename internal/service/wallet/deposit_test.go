package wallet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDepositValidatesRequestBeforeDatabaseAccess(t *testing.T) {
	service := NewService(nil)
	userID, walletID := uuid.New(), uuid.New()
	for _, test := range []struct {
		name     string
		amount   int64
		currency string
		key      string
		wantErr  error
	}{
		{name: "zero amount", amount: 0, currency: "USD", key: "key", wantErr: ErrInvalidAmount},
		{name: "negative amount", amount: -1, currency: "USD", key: "key", wantErr: ErrInvalidAmount},
		{name: "unsupported currency", amount: 1, currency: "CAD", key: "key", wantErr: ErrInvalidCurrency},
		{name: "empty key", amount: 1, currency: "USD", key: " ", wantErr: ErrInvalidIdempotencyKey},
		{name: "long key", amount: 1, currency: "USD", key: strings.Repeat("x", 129), wantErr: ErrInvalidIdempotencyKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := service.Deposit(context.Background(), userID, walletID, test.amount, test.currency, test.key)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Deposit() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestDepositRequestHashUsesNormalizedParameters(t *testing.T) {
	userID, walletID := uuid.New(), uuid.New()
	base := depositRequestHash(userID, walletID, 250, "USD")
	if got := depositRequestHash(userID, walletID, 250, "USD"); got != base {
		t.Fatal("same request produced a different hash")
	}
	if depositRequestHash(userID, walletID, 251, "USD") == base {
		t.Fatal("amount change did not alter request hash")
	}
	if depositRequestHash(userID, walletID, 250, "EUR") == base {
		t.Fatal("currency change did not alter request hash")
	}
	if depositRequestHash(userID, uuid.New(), 250, "USD") == base {
		t.Fatal("wallet change did not alter request hash")
	}
}
