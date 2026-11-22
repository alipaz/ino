package domain

import "time"

type ReelShare struct {
	ID             int64
	SenderID       int64
	RecipientID    int64
	ReelID         int64
	IdempotencyKey string
	CreatedAt      time.Time
}
