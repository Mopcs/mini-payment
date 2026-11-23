package postgres

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func claimLedgerTransaction(ctx context.Context, tx pgx.Tx, userID, transactionID uuid.UUID, transactionType, key string, requestHash []byte, sourceWalletID uuid.UUID, destinationWalletID *uuid.UUID, currency string, amount int64) (uuid.UUID, time.Time, bool, error) {
	var createdAt time.Time
	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions
			(id, transaction_type, user_id, idempotency_key, request_hash, wallet_id,
			 destination_wallet_id, currency, amount)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING id, created_at`,
		transactionID, transactionType, userID, key, requestHash, sourceWalletID,
		destinationWalletID, currency, amount,
	).Scan(&transactionID, &createdAt)
	if err == nil {
		return transactionID, createdAt, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, time.Time{}, false, translateError("claim transaction idempotency key", err)
	}

	var existingID uuid.UUID
	var existingType string
	var existingHash []byte
	err = tx.QueryRow(ctx, `
		SELECT id, transaction_type, request_hash
		FROM ledger_transactions
		WHERE user_id = $1 AND idempotency_key = $2`, userID, key,
	).Scan(&existingID, &existingType, &existingHash)
	if err != nil {
		return uuid.Nil, time.Time{}, false, translateError("load idempotent transaction", err)
	}
	if existingType != transactionType || !bytes.Equal(existingHash, requestHash) {
		return uuid.Nil, time.Time{}, false, ErrIdempotencyConflict
	}
	if err := tx.QueryRow(ctx, `SELECT created_at FROM ledger_transactions WHERE id = $1`, existingID).Scan(&createdAt); err != nil {
		return uuid.Nil, time.Time{}, false, translateError("load idempotent transaction timestamp", err)
	}
	return existingID, createdAt, true, nil
}
