package transfer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTransferValidatesRequestBeforeDatabaseAccess(t *testing.T) {
	service := NewService(nil)
	userID, sourceID, destinationID := uuid.New(), uuid.New(), uuid.New()
	for _, test := range []struct {
		name        string
		sourceID    uuid.UUID
		destination uuid.UUID
		amount      int64
		currency    string
		key         string
		wantErr     error
	}{
		{name: "zero amount", sourceID: sourceID, destination: destinationID, amount: 0, currency: "USD", key: "key", wantErr: ErrInvalidAmount},
		{name: "negative amount", sourceID: sourceID, destination: destinationID, amount: -1, currency: "USD", key: "key", wantErr: ErrInvalidAmount},
		{name: "same wallet", sourceID: sourceID, destination: sourceID, amount: 1, currency: "USD", key: "key", wantErr: ErrSameWallet},
		{name: "missing source ID", destination: destinationID, amount: 1, currency: "USD", key: "key", wantErr: ErrInvalidWalletID},
		{name: "missing destination ID", sourceID: sourceID, amount: 1, currency: "USD", key: "key", wantErr: ErrInvalidWalletID},
		{name: "unsupported currency", sourceID: sourceID, destination: destinationID, amount: 1, currency: "CAD", key: "key", wantErr: ErrInvalidCurrency},
		{name: "empty idempotency key", sourceID: sourceID, destination: destinationID, amount: 1, currency: "USD", key: " ", wantErr: ErrInvalidIdempotencyKey},
		{name: "long idempotency key", sourceID: sourceID, destination: destinationID, amount: 1, currency: "USD", key: strings.Repeat("x", 129), wantErr: ErrInvalidIdempotencyKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := service.Transfer(context.Background(), userID, test.sourceID, test.destination, test.amount, test.currency, test.key)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Transfer() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestTransferRequestHashUsesNormalizedParameters(t *testing.T) {
	userID, sourceID, destinationID := uuid.New(), uuid.New(), uuid.New()
	base := transferRequestHash(userID, sourceID, destinationID, 250, "USD")
	if transferRequestHash(userID, sourceID, destinationID, 250, "USD") != base {
		t.Fatal("same transfer produced a different hash")
	}
	if transferRequestHash(userID, sourceID, destinationID, 251, "USD") == base {
		t.Fatal("amount change did not alter request hash")
	}
	if transferRequestHash(userID, sourceID, destinationID, 250, "EUR") == base {
		t.Fatal("currency change did not alter request hash")
	}
	if transferRequestHash(userID, destinationID, sourceID, 250, "USD") == base {
		t.Fatal("direction change did not alter request hash")
	}
}
