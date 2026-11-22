package usecase

import (
	"context"
	"errors"
)

var ErrPlaybackReelConflict = errors.New("playback ID already bound to a different reel")

type ReelWatchSessionWriter interface {
	PersistWatchProgress(ctx context.Context, userID, reelID int64, playbackID string, watchedMilliseconds int64) (persistedWatchedMilliseconds, durationMilliseconds int64, err error)
}

type WatchProgress struct {
	WatchedMilliseconds int64
	WatchPercentage     float64
}

type ReelWatchSessions struct {
	writer ReelWatchSessionWriter
}

func NewReelWatchSessions(writer ReelWatchSessionWriter) *ReelWatchSessions {
	return &ReelWatchSessions{writer: writer}
}

func (uc *ReelWatchSessions) UpdateWatchProgress(ctx context.Context, userID, reelID int64, playbackID string, watchedMilliseconds int64) (WatchProgress, error) {
	persistedWatchedMilliseconds, durationMilliseconds, err := uc.writer.PersistWatchProgress(ctx, userID, reelID, playbackID, watchedMilliseconds)
	if err != nil {
		return WatchProgress{}, err
	}
	return WatchProgress{
		WatchedMilliseconds: persistedWatchedMilliseconds,
		WatchPercentage:     min(100, float64(persistedWatchedMilliseconds)/float64(durationMilliseconds)*100),
	}, nil
}
