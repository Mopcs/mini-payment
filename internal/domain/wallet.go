package domain

import (
	"time"

	"github.com/google/uuid"
)

type Wallet struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Currency  string
	Balance   int64 // minor units: cents for USD/EUR, whole yen for JPY
	CreatedAt time.Time
	UpdatedAt time.Time
}
