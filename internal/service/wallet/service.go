package wallet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

var (
	ErrNotFound        = errors.New("wallet not found")
	ErrAlreadyExists   = errors.New("wallet already exists")
	ErrInvalidCurrency = errors.New("unsupported currency")
)

type Service struct {
	repository *postgres.WalletRepository
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{repository: postgres.NewWalletRepository(pool)}
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
