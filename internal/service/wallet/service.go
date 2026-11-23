package wallet

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

var (
	ErrNotFound              = errors.New("wallet not found")
	ErrAlreadyExists         = errors.New("wallet already exists")
	ErrInvalidCurrency       = errors.New("unsupported currency")
	ErrInvalidAmount         = errors.New("amount must be positive")
	ErrInvalidIdempotencyKey = errors.New("idempotency key is invalid")
	ErrCurrencyMismatch      = errors.New("currency does not match wallet")
	ErrIdempotencyConflict   = errors.New("idempotency key conflicts with a different request")
	ErrLedgerAccountMissing  = errors.New("ledger account is missing")
	ErrBalanceLimit          = errors.New("wallet balance limit exceeded")
)

type Service struct {
	repository *postgres.WalletRepository
	deposits   *postgres.DepositRepository
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{repository: postgres.NewWalletRepository(pool), deposits: postgres.NewDepositRepository(pool)}
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]domain.Wallet, error) {
	wallets, err := s.repository.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list wallets: %w", err)
	}
	return wallets, nil
}

func (s *Service) Get(ctx context.Context, userID, walletID uuid.UUID) (*domain.Wallet, error) {
	wallet, err := s.repository.GetByIDAndUserID(ctx, walletID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get wallet: %w", err)
	}
	return wallet, nil
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, currency string) (*domain.Wallet, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	switch currency {
	case "USD", "EUR", "JPY":
	default:
		return nil, ErrInvalidCurrency
	}
	wallet := &domain.Wallet{ID: uuid.New(), UserID: userID, Currency: currency, Balance: 0}
	if err := s.repository.Create(ctx, wallet); err != nil {
		if errors.Is(err, postgres.ErrWalletAlreadyExists) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create wallet: %w", err)
	}
	return wallet, nil
}

func (s *Service) Deposit(ctx context.Context, userID, walletID uuid.UUID, amount int64, currency, idempotencyKey string) (domain.Deposit, bool, error) {
	if amount <= 0 {
		return domain.Deposit{}, false, ErrInvalidAmount
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	switch currency {
	case "USD", "EUR", "JPY":
	default:
		return domain.Deposit{}, false, ErrInvalidCurrency
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) == 0 || len(idempotencyKey) > 128 {
		return domain.Deposit{}, false, ErrInvalidIdempotencyKey
	}

	requestHash := depositRequestHash(userID, walletID, amount, currency)
	deposit, replay, err := s.deposits.Create(ctx, userID, walletID, amount, currency, idempotencyKey, requestHash[:])
	switch {
	case errors.Is(err, postgres.ErrDepositWalletNotFound):
		return domain.Deposit{}, false, ErrNotFound
	case errors.Is(err, postgres.ErrDepositCurrencyMismatch):
		return domain.Deposit{}, false, ErrCurrencyMismatch
	case errors.Is(err, postgres.ErrIdempotencyConflict):
		return domain.Deposit{}, false, ErrIdempotencyConflict
	case errors.Is(err, postgres.ErrLedgerAccountMissing):
		return domain.Deposit{}, false, ErrLedgerAccountMissing
	case errors.Is(err, postgres.ErrBalanceOverflow):
		return domain.Deposit{}, false, ErrBalanceLimit
	case err != nil:
		return domain.Deposit{}, false, fmt.Errorf("deposit funds: %w", err)
	default:
		return deposit, replay, nil
	}
}

func depositRequestHash(userID, walletID uuid.UUID, amount int64, currency string) [sha256.Size]byte {
	var request [16 + 16 + 8 + 3]byte
	copy(request[:16], userID[:])
	copy(request[16:32], walletID[:])
	binary.BigEndian.PutUint64(request[32:40], uint64(amount))
	copy(request[40:], currency)
	return sha256.Sum256(request[:])
}
