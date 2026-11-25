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
	// TraceParent and TraceState carry the W3C trace context that produced the
	// event. They stay out of Payload so the published contract is unchanged.
	TraceParent string
	TraceState  string
}

type TransferCompletedPayload struct {
	TransferID          uuid.UUID `json:"transfer_id"`
	SourceWalletID      uuid.UUID `json:"source_wallet_id"`
	DestinationWalletID uuid.UUID `json:"destination_wallet_id"`
	Amount              int64     `json:"amount"`
	Currency            string    `json:"currency"`
	CompletedAt         time.Time `json:"completed_at"`
}
