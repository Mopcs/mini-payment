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
