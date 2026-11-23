package domain

import (
	"time"

	"github.com/google/uuid"
)

type Deposit struct {
	TransactionID uuid.UUID
	WalletID      uuid.UUID
	Amount        int64
	Currency      string
	BalanceAfter  int64
	CreatedAt     time.Time
}
