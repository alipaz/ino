package usecase

import (
	"context"
	"time"

	"ino/internal/domain"
)

type ReelInteractionWriter interface {
	SetActiveReelInteraction(ctx context.Context, userID, reelID int64, kind domain.InteractionKind, active bool) error
	RecordReelView(ctx context.Context, userID, reelID int64, utcDate time.Time) error
}

type ReelInteractions struct {
	writer ReelInteractionWriter
}

func NewReelInteractions(writer ReelInteractionWriter) *ReelInteractions {
	return &ReelInteractions{writer: writer}
}

func (uc *ReelInteractions) SetReelLike(ctx context.Context, userID, reelID int64, active bool) error {
	return uc.writer.SetActiveReelInteraction(ctx, userID, reelID, domain.Like, active)
}

func (uc *ReelInteractions) SetReelSave(ctx context.Context, userID, reelID int64, active bool) error {
	return uc.writer.SetActiveReelInteraction(ctx, userID, reelID, domain.Save, active)
}

func (uc *ReelInteractions) RecordDailyReelView(ctx context.Context, userID, reelID int64) error {
	utcDate := time.Now().UTC().Truncate(24 * time.Hour)
	return uc.writer.RecordReelView(ctx, userID, reelID, utcDate)
}
