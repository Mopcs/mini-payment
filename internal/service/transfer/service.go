package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

const maxIdempotencyKeyBytes = 128

var (
	ErrInvalidAmount         = errors.New("transfer amount must be positive")
	ErrInvalidCurrency       = errors.New("unsupported transfer currency")
	ErrSameWallet            = errors.New("source and destination wallets must differ")
	ErrInvalidWalletID       = errors.New("source and destination wallet IDs are required")
	ErrInvalidIdempotencyKey = errors.New("idempotency key is invalid")
	ErrIdempotencyConflict   = errors.New("idempotency key conflicts with a different request")
	ErrNotFound              = errors.New("wallet not found")
	ErrInsufficientFunds     = errors.New("insufficient funds")
	ErrCurrencyMismatch      = errors.New("wallet currency mismatch")
	ErrBalanceLimit          = errors.New("wallet balance limit exceeded")
	ErrLedgerAccountMissing  = errors.New("ledger account is missing")
)

type Service struct {
	repository *postgres.TransferRepository
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{repository: postgres.NewTransferRepository(pool)}
}

func (s *Service) Transfer(ctx context.Context, userID, sourceWalletID, destinationWalletID uuid.UUID, amount int64, currency, idempotencyKey string) (domain.Transfer, bool, error) {
	if amount <= 0 {
		return domain.Transfer{}, false, ErrInvalidAmount
	}
	if sourceWalletID == uuid.Nil || destinationWalletID == uuid.Nil {
		return domain.Transfer{}, false, ErrInvalidWalletID
	}
	if sourceWalletID == destinationWalletID {
		return domain.Transfer{}, false, ErrSameWallet
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	switch currency {
	case "USD", "EUR", "JPY":
	default:
		return domain.Transfer{}, false, ErrInvalidCurrency
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) == 0 || len(idempotencyKey) > maxIdempotencyKeyBytes {
		return domain.Transfer{}, false, ErrInvalidIdempotencyKey
	}

	requestHash := transferRequestHash(userID, sourceWalletID, destinationWalletID, amount, currency)
	result, replay, err := s.repository.Create(ctx, userID, sourceWalletID, destinationWalletID, amount, currency, idempotencyKey, requestHash[:])
	switch {
	case errors.Is(err, postgres.ErrIdempotencyConflict):
		return domain.Transfer{}, false, ErrIdempotencyConflict
	case errors.Is(err, postgres.ErrTransferWalletNotFound), errors.Is(err, postgres.ErrTransferSourceNotOwned):
		return domain.Transfer{}, false, ErrNotFound
	case errors.Is(err, postgres.ErrTransferCurrencyMismatch):
		return domain.Transfer{}, false, ErrCurrencyMismatch
	case errors.Is(err, postgres.ErrInsufficientFunds):
		return domain.Transfer{}, false, ErrInsufficientFunds
	case errors.Is(err, postgres.ErrBalanceOverflow):
		return domain.Transfer{}, false, ErrBalanceLimit
	case errors.Is(err, postgres.ErrLedgerAccountMissing):
		return domain.Transfer{}, false, ErrLedgerAccountMissing
	case err != nil:
		return domain.Transfer{}, false, fmt.Errorf("execute transfer: %w", err)
	default:
		return result, replay, nil
	}
}

func transferRequestHash(userID, sourceWalletID, destinationWalletID uuid.UUID, amount int64, currency string) [sha256.Size]byte {
	var request [8 + 16 + 16 + 16 + 8 + 3]byte
	copy(request[:8], "transfer")
	copy(request[8:24], userID[:])
	copy(request[24:40], sourceWalletID[:])
	copy(request[40:56], destinationWalletID[:])
	binary.BigEndian.PutUint64(request[56:64], uint64(amount))
	copy(request[64:], currency)
	return sha256.Sum256(request[:])
}
