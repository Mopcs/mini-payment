package postgres

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ledgerTransaction is the row claimLedgerTransaction reserves. Passing a struct
// keeps the call sites readable and makes it impossible to swap amount and
// currency by accident.
type ledgerTransaction struct {
	id                  uuid.UUID
	userID              uuid.UUID
	transactionType     string
	idempotencyKey      string
	requestHash         []byte
	sourceWalletID      uuid.UUID
	destinationWalletID *uuid.UUID
	currency            string
	amount              int64
}

// claimLedgerTransaction reserves (user_id, idempotency_key) for a new ledger
// row. If the key is already taken it compares the stored request hash: a match
// means the caller is retrying and should get the original result back, a
// mismatch is a conflict. The bool result reports a replay.
func claimLedgerTransaction(
	ctx context.Context,
	tx pgx.Tx,
	entry ledgerTransaction,
) (uuid.UUID, time.Time, bool, error) {
	var transactionID uuid.UUID
	var createdAt time.Time

	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions
			(id, transaction_type, user_id, idempotency_key, request_hash, wallet_id,
			 destination_wallet_id, currency, amount)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING id, created_at`,
		entry.id, entry.transactionType, entry.userID, entry.idempotencyKey,
		entry.requestHash, entry.sourceWalletID, entry.destinationWalletID,
		entry.currency, entry.amount,
	).Scan(&transactionID, &createdAt)
	if err == nil {
		return transactionID, createdAt, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, time.Time{}, false, translateError("claim transaction idempotency key", err)
	}
	return replayLedgerTransaction(ctx, tx, entry)
}

// replayLedgerTransaction handles the conflict branch: the row is there, so the
// question is whether it belongs to this request.
func replayLedgerTransaction(
	ctx context.Context,
	tx pgx.Tx,
	entry ledgerTransaction,
) (uuid.UUID, time.Time, bool, error) {
	var existingID uuid.UUID
	var existingType string
	var existingHash []byte
	var createdAt time.Time

	err := tx.QueryRow(ctx, `
		SELECT id, transaction_type, request_hash, created_at
		FROM ledger_transactions
		WHERE user_id = $1 AND idempotency_key = $2`,
		entry.userID, entry.idempotencyKey,
	).Scan(&existingID, &existingType, &existingHash, &createdAt)
	if err != nil {
		return uuid.Nil, time.Time{}, false, translateError("load idempotent transaction", err)
	}
	if existingType != entry.transactionType || !bytes.Equal(existingHash, entry.requestHash) {
		return uuid.Nil, time.Time{}, false, ErrIdempotencyConflict
	}
	return existingID, createdAt, true, nil
}
