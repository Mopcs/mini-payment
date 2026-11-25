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
	"github.com/Mopcs/mini-payment-system/internal/observability"
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

func (s *Service) Transfer(
	ctx context.Context,
	userID, sourceWalletID, destinationWalletID uuid.UUID,
	amount int64,
	currency, idempotencyKey string,
) (domain.Transfer, bool, error) {
	ctx, span := observability.Tracer().Start(ctx, "transfer.validate")
	currency, idempotencyKey, err := validateTransfer(
		sourceWalletID, destinationWalletID, amount, currency, idempotencyKey,
	)
	observability.RecordError(span, err)
	span.End()
	if err != nil {
		return domain.Transfer{}, false, err
	}

	// Same hashing scheme as deposits, except the type tag goes into the input.
	// That way a key used for a deposit can never be replayed as a transfer.
	requestHash := transferRequestHash(
		userID, sourceWalletID, destinationWalletID, amount, currency,
	)
	result, replay, err := s.repository.Create(ctx, postgres.TransferRequest{
		UserID:         userID,
		SourceID:       sourceWalletID,
		DestinationID:  destinationWalletID,
		Amount:         amount,
		Currency:       currency,
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash[:],
	})
	if err != nil {
		return domain.Transfer{}, false, translateTransferError(err)
	}
	return result, replay, nil
}

// validateTransfer checks the request and returns the normalized currency and
// idempotency key.
func validateTransfer(
	sourceWalletID, destinationWalletID uuid.UUID,
	amount int64,
	currency, idempotencyKey string,
) (string, string, error) {
	if amount <= 0 {
		return "", "", ErrInvalidAmount
	}
	if sourceWalletID == uuid.Nil || destinationWalletID == uuid.Nil {
		return "", "", ErrInvalidWalletID
	}
	if sourceWalletID == destinationWalletID {
		return "", "", ErrSameWallet
	}

	currency = domain.NormalizeCurrency(currency)
	if !domain.IsSupportedCurrency(currency) {
		return "", "", ErrInvalidCurrency
	}

	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) == 0 || len(idempotencyKey) > maxIdempotencyKeyBytes {
		return "", "", ErrInvalidIdempotencyKey
	}
	return currency, idempotencyKey, nil
}

// translateTransferError maps repository outcomes onto the errors handlers
// already know how to answer.
func translateTransferError(err error) error {
	switch {
	case errors.Is(err, postgres.ErrIdempotencyConflict):
		return ErrIdempotencyConflict
	case errors.Is(err, postgres.ErrTransferWalletNotFound),
		errors.Is(err, postgres.ErrTransferSourceNotOwned),
		errors.Is(err, postgres.ErrWalletBalanceChanged):
		return ErrNotFound
	case errors.Is(err, postgres.ErrTransferCurrencyMismatch):
		return ErrCurrencyMismatch
	case errors.Is(err, postgres.ErrInsufficientFunds):
		return ErrInsufficientFunds
	case errors.Is(err, postgres.ErrBalanceOverflow):
		return ErrBalanceLimit
	case errors.Is(err, postgres.ErrLedgerAccountMissing):
		return ErrLedgerAccountMissing
	default:
		return fmt.Errorf("execute transfer: %w", err)
	}
}

func (s *Service) List(
	ctx context.Context,
	userID uuid.UUID,
	filter domain.TransferHistoryFilter,
) ([]domain.TransferHistoryItem, int64, error) {
	items, total, err := s.repository.List(ctx, userID, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("list transfer history: %w", err)
	}
	return items, total, nil
}

func transferRequestHash(
	userID, sourceWalletID, destinationWalletID uuid.UUID,
	amount int64,
	currency string,
) [sha256.Size]byte {
	var request [8 + 16 + 16 + 16 + 8 + 3]byte
	copy(request[:8], "transfer")
	copy(request[8:24], userID[:])
	copy(request[24:40], sourceWalletID[:])
	copy(request[40:56], destinationWalletID[:])
	binary.BigEndian.PutUint64(request[56:64], uint64(amount))
	copy(request[64:], currency)
	return sha256.Sum256(request[:])
}
