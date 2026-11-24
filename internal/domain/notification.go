package domain

import "github.com/google/uuid"

const NotificationTypeTransferCompleted = "transfer.completed"

type TransferNotificationPayload struct {
	TransferID uuid.UUID `json:"transfer_id"`
	Amount     int64     `json:"amount"`
	Currency   string    `json:"currency"`
	Direction  string    `json:"direction"`
}
