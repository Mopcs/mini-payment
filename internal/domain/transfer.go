package domain

import (
	"time"

	"github.com/google/uuid"
)

type Transfer struct {
	TransactionID           uuid.UUID
	SourceWalletID          uuid.UUID
	DestinationWalletID     uuid.UUID
	Amount                  int64
	Currency                string
	SourceBalanceAfter      int64
	DestinationBalanceAfter int64
	CreatedAt               time.Time
}

type TransferHistoryFilter struct {
	Direction string
	Currency  string
	Limit     int
	Offset    int
}

type TransferHistoryItem struct {
	ID                  uuid.UUID `json:"id"`
	SourceWalletID      uuid.UUID `json:"source_wallet_id"`
	DestinationWalletID uuid.UUID `json:"destination_wallet_id"`
	Amount              int64     `json:"amount"`
	Currency            string    `json:"currency"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
}
