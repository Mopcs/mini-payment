package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

// walletColumns keeps the SELECT list and the scan order in one place.
const walletColumns = `id, user_id, currency, balance, created_at, updated_at`

type WalletRepository struct {
	db DBTX
}

func NewWalletRepository(db DBTX) *WalletRepository {
	return &WalletRepository{db: db}
}

func (r *WalletRepository) Create(ctx context.Context, wallet *domain.Wallet) error {
	if wallet == nil {
		return ErrInvalidInput
	}
	if wallet.ID == uuid.Nil {
		wallet.ID = uuid.New()
	}
	wallet.Currency = domain.NormalizeCurrency(wallet.Currency)

	err := r.db.QueryRow(ctx, `
		INSERT INTO wallets (id, user_id, currency, balance)
		VALUES ($1, $2, $3, $4)
		RETURNING `+walletColumns,
		wallet.ID, wallet.UserID, wallet.Currency, wallet.Balance,
	).Scan(
		&wallet.ID,
		&wallet.UserID,
		&wallet.Currency,
		&wallet.Balance,
		&wallet.CreatedAt,
		&wallet.UpdatedAt,
	)
	return translateError("create wallet", err)
}

func (r *WalletRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	wallet, err := scanWallet(r.db.QueryRow(ctx, `
		SELECT `+walletColumns+`
		FROM wallets
		WHERE id = $1`, id))
	if err != nil {
		return nil, translateError("get wallet by id", err)
	}
	return wallet, nil
}

func (r *WalletRepository) GetByIDAndUserID(
	ctx context.Context,
	id, userID uuid.UUID,
) (*domain.Wallet, error) {
	wallet, err := scanWallet(r.db.QueryRow(ctx, `
		SELECT `+walletColumns+`
		FROM wallets
		WHERE id = $1 AND user_id = $2`, id, userID))
	if err != nil {
		return nil, translateError("get wallet by id and user", err)
	}
	return wallet, nil
}

func (r *WalletRepository) GetByUserID(
	ctx context.Context,
	userID uuid.UUID,
) ([]domain.Wallet, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+walletColumns+`
		FROM wallets
		WHERE user_id = $1
		ORDER BY currency ASC, id ASC`, userID)
	if err != nil {
		return nil, translateError("list wallets by user", err)
	}
	defer rows.Close()

	wallets := make([]domain.Wallet, 0)
	for rows.Next() {
		wallet, err := scanWallet(rows)
		if err != nil {
			return nil, translateError("scan wallets by user", err)
		}
		wallets = append(wallets, *wallet)
	}
	if err := rows.Err(); err != nil {
		return nil, translateError("iterate wallets by user", err)
	}
	return wallets, nil
}

func (r *WalletRepository) GetByUserIDAndCurrency(
	ctx context.Context,
	userID uuid.UUID,
	currency string,
) (*domain.Wallet, error) {
	wallet, err := scanWallet(r.db.QueryRow(ctx, `
		SELECT `+walletColumns+`
		FROM wallets
		WHERE user_id = $1 AND currency = $2`, userID, domain.NormalizeCurrency(currency)))
	if err != nil {
		return nil, translateError("get wallet by user and currency", err)
	}
	return wallet, nil
}

func scanWallet(row rowScanner) (*domain.Wallet, error) {
	wallet := new(domain.Wallet)
	err := row.Scan(
		&wallet.ID,
		&wallet.UserID,
		&wallet.Currency,
		&wallet.Balance,
		&wallet.CreatedAt,
		&wallet.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return wallet, nil
}
