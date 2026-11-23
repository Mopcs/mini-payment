package postgres

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

type WalletRepository struct {
	pool *pgxpool.Pool
}

func NewWalletRepository(pool *pgxpool.Pool) *WalletRepository {
	return &WalletRepository{pool: pool}
}

func (r *WalletRepository) Create(ctx context.Context, wallet *domain.Wallet) error {
	if wallet == nil {
		return ErrInvalidInput
	}
	if wallet.ID == uuid.Nil {
		wallet.ID = uuid.New()
	}
	wallet.Currency = normalizeCurrency(wallet.Currency)

	err := r.pool.QueryRow(ctx, `
		INSERT INTO wallets (id, user_id, currency, balance)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, currency, balance, created_at, updated_at`,
		wallet.ID, wallet.UserID, wallet.Currency, wallet.Balance,
	).Scan(&wallet.ID, &wallet.UserID, &wallet.Currency, &wallet.Balance, &wallet.CreatedAt, &wallet.UpdatedAt)
	return translateError("create wallet", err)
}

func (r *WalletRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	wallet := new(domain.Wallet)
	err := r.pool.QueryRow(ctx, `
		SELECT id, user_id, currency, balance, created_at, updated_at
		FROM wallets
		WHERE id = $1`, id,
	).Scan(&wallet.ID, &wallet.UserID, &wallet.Currency, &wallet.Balance, &wallet.CreatedAt, &wallet.UpdatedAt)
	if err != nil {
		return nil, translateError("get wallet by id", err)
	}
	return wallet, nil
}

func (r *WalletRepository) GetByUserID(ctx context.Context, userID uuid.UUID) ([]domain.Wallet, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id, currency, balance, created_at, updated_at
		FROM wallets
		WHERE user_id = $1
		ORDER BY currency ASC, id ASC`, userID)
	if err != nil {
		return nil, translateError("list wallets by user", err)
	}
	defer rows.Close()

	wallets := make([]domain.Wallet, 0)
	for rows.Next() {
		var wallet domain.Wallet
		if err := rows.Scan(&wallet.ID, &wallet.UserID, &wallet.Currency, &wallet.Balance, &wallet.CreatedAt, &wallet.UpdatedAt); err != nil {
			return nil, translateError("scan wallets by user", err)
		}
		wallets = append(wallets, wallet)
	}
	if err := rows.Err(); err != nil {
		return nil, translateError("iterate wallets by user", err)
	}
	return wallets, nil
}

func (r *WalletRepository) GetByUserIDAndCurrency(ctx context.Context, userID uuid.UUID, currency string) (*domain.Wallet, error) {
	wallet := new(domain.Wallet)
	err := r.pool.QueryRow(ctx, `
		SELECT id, user_id, currency, balance, created_at, updated_at
		FROM wallets
		WHERE user_id = $1 AND currency = $2`, userID, normalizeCurrency(currency),
	).Scan(&wallet.ID, &wallet.UserID, &wallet.Currency, &wallet.Balance, &wallet.CreatedAt, &wallet.UpdatedAt)
	if err != nil {
		return nil, translateError("get wallet by user and currency", err)
	}
	return wallet, nil
}

func normalizeCurrency(currency string) string {
	return strings.ToUpper(strings.TrimSpace(currency))
}
