package domain

import (
	"time"

	"github.com/google/uuid"
)

type Wallet struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Currency  string
	Balance   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}
