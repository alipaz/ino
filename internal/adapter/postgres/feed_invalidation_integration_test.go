package postgres_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"ino/internal/adapter/postgres"
	cache "ino/internal/adapter/redis"
	"ino/internal/domain"
)

func TestFeedOrderInvalidatesOnlyTheAffectedViewerPostgresRedis(t *testing.T) {
	ctx, pool, redisClient, activity := openPostgresRedisTest(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO products (title, brand, image_url, price, category) VALUES ('Boots', 'INO', 'boots.png', 100, 'outdoors');
		INSERT INTO reel_products (reel_id, product_id, display_position) VALUES (1, 1, 0);`); err != nil {
		t.Fatal(err)
	}
	readCache := newTestReadCache(redisClient)
	reader := postgres.NewFeedReader(pool)
	feed := cache.NewCachedFeedReader(readCache, reader)
	projector := cache.NewRecentInteractionProjector(readCache, activity)
	drainCacheOutbox(t, ctx, pool, projector.Apply)

	type orderScope struct {
		viewer   int64
		category string
	}
	scopes := []orderScope{{0, ""}, {1, ""}, {2, ""}, {1, "outdoors"}}
	loads := make(map[orderScope]int)
	read := func(scope orderScope) []int64 {
		t.Helper()
		ids, err := feed.ReadFeedOrder(ctx, scope.viewer, scope.category, domain.FeedRankingVersion, func(ctx context.Context) ([]int64, error) {
			loads[scope]++
			candidates, err := reader.LoadFeedCandidates(ctx, scope.category, nil)
			if err != nil {
				return nil, err
			}
			ids := make([]int64, 0, len(candidates))
			for _, candidate := range candidates {
				ids = append(ids, candidate.ID)
			}
			slices.Sort(ids)
			return ids, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	for _, scope := range scopes {
		if ids := read(scope); !slices.Contains(ids, int64(1)) {
			t.Fatalf("warm order for %+v is missing reel 1: %v", scope, ids)
		}
	}

	writer := postgres.NewReelWriter(pool)
	if err := writer.RecordReelView(ctx, 1, 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	for _, scope := range scopes {
		_ = read(scope)
		wantLoads := 1
		if scope.viewer == 1 {
			wantLoads = 2
		}
		if got := loads[scope]; got != wantLoads {
			t.Errorf("order for %+v loaded %d times, want %d", scope, got, wantLoads)
		}
	}
	recent := cache.NewCachedInteractionReader(readCache, activity, activity)
	events, err := recent.ListRecentInteractions(ctx, 1)
	if err != nil || len(events) != 1 || events[0].Type != "view_counted" || events[0].ReelID != 1 {
		t.Fatalf("recent activity = %+v, err=%v, want one counted view", events, err)
	}

	if _, err := pool.Exec(ctx, "UPDATE reels SET status = 'archived' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	for _, scope := range scopes {
		before := loads[scope]
		if got := read(scope); slices.Contains(got, int64(1)) || loads[scope] != before+1 {
			t.Errorf("catalog change did not refresh %+v: ids=%v loads=%d", scope, got, loads[scope])
		}
	}
}

func TestFeedOrderRejectsFillsBeforeViewerOrCatalogInvalidationPostgresRedis(t *testing.T) {
	for _, invalidation := range []string{"viewer", "catalog"} {
		t.Run(invalidation, func(t *testing.T) {
			ctx, pool, redisClient, activity := openPostgresRedisTest(t)
			readCache := newTestReadCache(redisClient)
			feed := cache.NewCachedFeedReader(readCache, postgres.NewFeedReader(pool))
			projector := cache.NewRecentInteractionProjector(readCache, activity)
			drainCacheOutbox(t, ctx, pool, projector.Apply)

			started := make(chan struct{})
			release := make(chan struct{})
			filled := make(chan error, 1)
			go func() {
				_, err := feed.ReadFeedOrder(ctx, 1, "", domain.FeedRankingVersion, func(ctx context.Context) ([]int64, error) {
					close(started)
					select {
					case <-release:
						return []int64{1, 2}, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				})
				filled <- err
			}()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
					<-filled
				}
			}()
			select {
			case <-started:
			case err := <-filled:
				close(release)
				t.Fatalf("stale read did not enter fallback: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}

			if invalidation == "viewer" {
				if err := postgres.NewReelWriter(pool).SetActiveReelInteraction(ctx, 1, 2, domain.Save, true); err != nil {
					t.Fatal(err)
				}
			} else if _, err := pool.Exec(ctx, "UPDATE reels SET status = 'archived' WHERE id = 1"); err != nil {
				t.Fatal(err)
			}
			drainCacheOutbox(t, ctx, pool, projector.Apply)
			close(release)
			if err := <-filled; err != nil {
				t.Fatalf("paused read failed: %v", err)
			}

			expected := []int64{2}
			if invalidation == "viewer" {
				expected = []int64{2, 1}
			}
			got, err := feed.ReadFeedOrder(ctx, 1, "", domain.FeedRankingVersion, func(context.Context) ([]int64, error) {
				return expected, nil
			})
			if err != nil || !slices.Equal(got, expected) {
				t.Fatalf("stale fill survived %s invalidation: ids=%v err=%v want=%v", invalidation, got, err, expected)
			}
		})
	}
}
