package usecase

import (
	"context"
	"errors"

	"ino/internal/domain"
)

var (
	ErrSelfShare         = errors.New("recipient is the sender")
	ErrRecipientNotFound = errors.New("share recipient not found")
	ErrShareKeyConflict  = errors.New("share key already used for a different payload")
)

type ReelShareWriter interface {
	RecordReelShare(ctx context.Context, senderID, reelID, recipientID int64, idempotencyKey string) (domain.ReelShare, error)
}

type ReelShares struct {
	writer ReelShareWriter
}

func NewReelShares(writer ReelShareWriter) *ReelShares {
	return &ReelShares{writer: writer}
}

func (uc *ReelShares) ShareReel(ctx context.Context, senderID, reelID, recipientID int64, idempotencyKey string) (domain.ReelShare, error) {
	if senderID == recipientID {
		return domain.ReelShare{}, ErrSelfShare
	}
	return uc.writer.RecordReelShare(ctx, senderID, reelID, recipientID, idempotencyKey)
}
