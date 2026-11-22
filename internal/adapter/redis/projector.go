package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ino/internal/domain"
	"ino/internal/usecase"
)

type RecentInteractionProjector struct {
	cache  *ReadCache
	recent usecase.RecentInteractionReader
}

func NewRecentInteractionProjector(cache *ReadCache, recent usecase.RecentInteractionReader) *RecentInteractionProjector {
	return &RecentInteractionProjector{cache: cache, recent: recent}
}

func (projector *RecentInteractionProjector) Apply(ctx context.Context, changes []domain.CacheChange) error {
	cache := projector.cache
	if cache.client == nil {
		return errors.New("Redis is disabled")
	}
	invalidations := make(map[string]struct{})
	activityUsers := make(map[int64]struct{})
	var oldest time.Time
	for _, change := range changes {
		switch change.Kind {
		case "catalog":
			invalidations[catalogEpochKey] = struct{}{}
		case "state":
			invalidations[interactionKey(change.UserID, change.ReelID).name] = struct{}{}
		case "identity":
			invalidations[identityKey(change.UserID).name] = struct{}{}
		case "activity":
			invalidations[feedViewerEpochKey(change.UserID)] = struct{}{}
			key := recentKey(change.UserID)
			invalidations[key.name] = struct{}{}
			activityUsers[change.UserID] = struct{}{}
		default:
			return fmt.Errorf("unknown cache change kind %q", change.Kind)
		}
		if oldest.IsZero() || change.CreatedAt.Before(oldest) {
			oldest = change.CreatedAt
		}
	}
	if len(invalidations) > 0 {
		keys := make([]string, 0, len(invalidations))
		for key := range invalidations {
			keys = append(keys, key)
		}
		redisCtx, cancel := context.WithTimeout(ctx, cache.timeout)
		err := cache.client.Del(redisCtx, keys...).Err()
		cancel()
		if err != nil {
			return fmt.Errorf("invalidate Redis snapshots: %w", err)
		}
	}

	// Build a complete snapshot from durable history every time. Even a delayed
	// or repeated notification sees the latest committed events. One guarded
	// Redis write replaces the whole snapshot, including truncation to 100.
	for userID := range activityUsers {
		key := recentKey(userID)
		read, err := cache.read(ctx, key)
		if err != nil {
			return fmt.Errorf("reserve recent activity generation: %w", err)
		}
		snapshot, err := readPostgres(ctx, cache, key.kind, func(ctx context.Context) (activitySnapshot, error) {
			return loadActivity(ctx, projector.recent, userID)
		})
		if err != nil {
			return err
		}
		outcome, err := cache.fill(ctx, key, read, snapshot, recentTTL)
		if err != nil {
			return fmt.Errorf("project recent activity: %w", err)
		}
		switch outcome {
		case fillInstalled, fillAlreadyCurrent:
			// Either this write installed the complete snapshot or a reader
			// installed the exact same bytes in the same generation.
		default:
			return errors.New("recent activity snapshot changed during projection")
		}
	}

	if len(changes) > 0 {
		cache.logger.Debug("cache projector batch", "events", len(changes), "lag_ms", time.Since(oldest).Milliseconds())
	}
	return nil
}

func (projector *RecentInteractionProjector) Run(ctx context.Context, process func(context.Context, func(context.Context, []domain.CacheChange) error) (int, error)) {
	if projector.cache.client == nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		count, err := process(ctx, projector.Apply)
		delay := 250 * time.Millisecond
		if err != nil {
			projector.cache.logger.Debug("cache projector retry", "outcome", "failed")
			delay = time.Second
		} else if count > 0 {
			delay = 0
		}
		timer.Reset(delay)
	}
}
