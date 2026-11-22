package usecase

import (
	"context"
	"errors"
	"fmt"

	"ino/internal/domain"
)

// ErrReadUnavailable signals that the authoritative read could not run.
var ErrReadUnavailable = errors.New("read service temporarily unavailable")

type InteractionStateReader interface {
	// The boolean reports whether the reel exists and is published.
	ReadInteractionState(ctx context.Context, userID, reelID int64) (domain.InteractionState, bool, error)
}

type RecentInteractionReader interface {
	// ListRecentInteractions reads up to 100 events, newest first, breaking time ties by descending ID.
	ListRecentInteractions(ctx context.Context, userID int64) ([]domain.ActivityEvent, error)
}

type InteractionReads struct {
	state  InteractionStateReader
	recent RecentInteractionReader
}

func NewInteractionReads(state InteractionStateReader, recent RecentInteractionReader) *InteractionReads {
	return &InteractionReads{state: state, recent: recent}
}

func (reads *InteractionReads) ReelState(ctx context.Context, userID, reelID int64) (domain.InteractionState, error) {
	state, published, err := reads.state.ReadInteractionState(ctx, userID, reelID)
	if err != nil {
		return domain.InteractionState{}, fmt.Errorf("read reel interaction state: %w", err)
	}
	if !published {
		return domain.InteractionState{}, ErrReelNotFound
	}
	return state, nil
}

func (reads *InteractionReads) Recent(ctx context.Context, userID int64) ([]domain.ActivityEvent, error) {
	events, err := reads.recent.ListRecentInteractions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list recent interactions: %w", err)
	}
	return events, nil
}
