package domain

import (
	"time"

	"github.com/google/uuid"
)

const EventTypeTransferCompleted = "transfer.completed"

type OutboxEvent struct {
	ID            uuid.UUID
	AggregateID   uuid.UUID
	AggregateType string
	EventType     string
	Payload       TransferCompletedPayload
	Attempts      int
}

type TransferCompletedPayload struct {
	TransferID          uuid.UUID `json:"transfer_id"`
	SourceWalletID      uuid.UUID `json:"source_wallet_id"`
	DestinationWalletID uuid.UUID `json:"destination_wallet_id"`
	Amount              int64     `json:"amount"`
	Currency            string    `json:"currency"`
	CompletedAt         time.Time `json:"completed_at"`
}
