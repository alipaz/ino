package postgres_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"ino/internal/adapter/postgres"
	"ino/internal/domain"
)

func feedFixture(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	pool := openCatalogTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	const fixtures = `
		INSERT INTO users (id, username, email) VALUES
			(1, 'one', 'one@example.com'), (2, 'two', 'two@example.com'),
			(3, 'three', 'three@example.com'), (4, 'four', 'four@example.com'),
			(5, 'five', 'five@example.com');
		INSERT INTO creators (id, display_name) VALUES (1, 'Creator');
		INSERT INTO reels (id, creator_id, caption, video_url, thumbnail_url, status, duration_ms,
			created_at, like_count, save_count, view_count) VALUES
			(1, 1, 'Main', 'video1', 'thumb1', 'published', 1000, '2026-11-20 10:00:00+00', 3, 2, 500),
			(2, 1, 'Other category', 'video2', 'thumb2', 'published', 1000, '2026-11-21 10:00:00+00', 0, 0, 0),
			(3, 1, 'Draft', 'video3', 'thumb3', 'draft', 1000, '2026-11-23 10:00:00+00', 100, 0, 0),
			(4, 1, 'No products', 'video4', 'thumb4', 'published', 1000, '2026-11-22 10:00:00+00', 0, 0, 0),
			(5, 1, 'Second matching', 'video5', 'thumb5', 'published', 1000, '2026-11-23 10:00:00+00', 0, 0, 0);
		INSERT INTO products (id, title, brand, image_url, price, category) VALUES
			(11, 'Bottle', 'INO', 'bottle', 800, 'drinkware'),
			(12, 'Mug', 'INO', 'mug', 900, 'drinkware'),
			(13, 'Hat', 'INO', 'hat', 1900, 'apparel');
		INSERT INTO reel_products (reel_id, product_id, display_position) VALUES
			(1, 12, 5), (1, 11, 5), (1, 13, 0), (2, 13, 0), (3, 11, 0), (5, 12, 0);
		INSERT INTO reel_shares (sender_id, recipient_id, reel_id, idempotency_key) VALUES
			(1, 2, 1, 'share-one'), (1, 3, 1, 'share-two'), (2, 1, 1, 'share-three');
		INSERT INTO user_reel_watch_sessions (user_id, reel_id, playback_id, watched_ms) VALUES
			(1, 1, 'quick', 199), (2, 1, 'exact20', 200), (3, 1, 'below70', 699),
			(4, 1, 'exact70', 700), (4, 1, 'short-replay', 10), (5, 1, 'quick-again', 5),
			(5, 1, 'also-quick', 100);
		UPDATE user_reel_watch_sessions SET watched_ms = GREATEST(watched_ms, 700)
			WHERE user_id = 4 AND playback_id = 'exact70';`
	if _, err := pool.Exec(ctx, fixtures); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

func TestFeedCandidateSignalsAndMaterializationPostgres(t *testing.T) {
	ctx, pool := feedFixture(t)
	reader := postgres.NewFeedReader(pool)

	candidates, err := reader.LoadFeedCandidates(ctx, "drinkware", []string{"apparel"})
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[int64]domain.FeedCandidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	if len(candidates) != 2 || byID[1].ID != 1 || byID[5].ID != 5 {
		t.Fatalf("drinkware candidates include missing/non-published reels: %+v", candidates)
	}
	first := byID[1]
	if first.Likes != 3 || first.Saves != 2 || first.Views != 500 || first.Shares != 2 ||
		first.QualityWatches != 1 || first.QuickSkips != 2 ||
		!reflect.DeepEqual(first.Categories, []string{"apparel", "drinkware"}) {
		t.Fatalf("ranking facts: %+v", first)
	}

	ids := []int64{3, 2, 1, 5, 4}
	page, err := reader.LoadFeedPage(ctx, "drinkware", ids, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Consumed != 3 {
		t.Fatalf("first page: %+v", page)
	}
	item := page.Items[0]
	if item.ID != 1 || item.Caption != "Main" || item.VideoURL != "video1" || item.ThumbnailURL != "thumb1" ||
		item.CreatedAt.Format(time.RFC3339) != "2026-11-20T10:00:00Z" || item.DurationMS != 1000 || item.LikeCount != 3 ||
		item.SaveCount != 2 || item.ViewCount != 500 || item.CreatorID != 1 || item.CreatorName != "Creator" {
		t.Fatalf("feed metadata: %+v", item)
	}
	wantProducts := []domain.Product{
		{ID: 13, Title: "Hat", Brand: "INO", ImageURL: "hat", Price: 1900, Category: "apparel"},
		{ID: 11, Title: "Bottle", Brand: "INO", ImageURL: "bottle", Price: 800, Category: "drinkware"},
		{ID: 12, Title: "Mug", Brand: "INO", ImageURL: "mug", Price: 900, Category: "drinkware"},
	}
	if !reflect.DeepEqual(item.Products, wantProducts) {
		t.Fatalf("feed products: got %+v, want %+v", item.Products, wantProducts)
	}

	remaining := ids[page.Consumed:]
	page, err = reader.LoadFeedPage(ctx, "drinkware", remaining, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != 5 || page.Consumed != len(remaining) {
		t.Fatalf("final page: %+v, err=%v", page, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE reels SET status = 'archived' WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	page, err = reader.LoadFeedPage(ctx, "drinkware", remaining, 1)
	if err != nil || len(page.Items) != 0 || page.Consumed != len(remaining) {
		t.Fatalf("after archival: %+v, err=%v", page, err)
	}
	page, err = reader.LoadFeedPage(ctx, "", []int64{4}, 1)
	if err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].Products, []domain.Product{}) {
		t.Fatalf("empty products: %+v, err=%v", page, err)
	}
}

func TestFeedPersonalInputsUseCurrentStateAndBoundedSeparateHistoriesPostgres(t *testing.T) {
	ctx, pool := feedFixture(t)
	const fixtures = `
		INSERT INTO user_interactions (user_id, reel_id, kind) VALUES (4, 1, 'like'), (4, 2, 'save');
		DELETE FROM user_interactions WHERE user_id = 4 AND reel_id = 1 AND kind = 'like';
		INSERT INTO activity_events (user_id, reel_id, event_type, event_time)
			SELECT 4, 1, 'share_created', '2025-01-01 00:00:00+00'::timestamptz - generate_series(1, 100) * interval '1 second';
		INSERT INTO reels (id, creator_id, caption, video_url, thumbnail_url, status, duration_ms, created_at)
			SELECT id, 1, '', '', '', 'published', 1000, '2026-11-23 11:00:00+00'::timestamptz
			FROM generate_series(10, 111) id;
		INSERT INTO user_reel_watch_sessions (user_id, reel_id, playback_id, watched_ms)
			SELECT 5, id, 'bound-' || id, 200 FROM generate_series(10, 111) id;`
	if _, err := pool.Exec(ctx, fixtures); err != nil {
		t.Fatal(err)
	}
	reader := postgres.NewFeedReader(pool)
	signals, err := reader.LoadFeedSignals(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals.Activity) > 100 || len(signals.Watches) != 1 ||
		signals.Watches[0].ReelID != 1 || signals.Watches[0].WatchedMS != 700 ||
		!reflect.DeepEqual(signals.Watches[0].Categories, []string{"apparel", "drinkware"}) {
		t.Fatalf("personal signal bounds or watch maximum: activity=%d watches=%+v", len(signals.Activity), signals.Watches)
	}
	var events []string
	for _, activity := range signals.Activity {
		if activity.Type != "share_created" {
			events = append(events, fmt.Sprintf("%d:%s:%s", activity.ReelID, activity.Type, strings.Join(activity.Categories, ",")))
		}
	}
	want := []string{"1:like_removed:apparel,drinkware", "2:save_added:apparel", "1:watch_interest:apparel,drinkware"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("personal activity = %v, want %v", events, want)
	}
	signals, err = reader.LoadFeedSignals(ctx, 5)
	if err != nil || len(signals.Watches) != 100 ||
		signals.Watches[0].ReelID != 111 || signals.Watches[99].ReelID != 12 {
		t.Fatalf("bounded watch input = %+v, err=%v", signals.Watches, err)
	}
}

func TestFeedCandidatePoolsReserveRoomForPreferencesPostgres(t *testing.T) {
	pool := openCatalogTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const fixtures = `
		INSERT INTO creators (id, display_name) VALUES (1, 'Creator');
		INSERT INTO reels (id, creator_id, caption, video_url, thumbnail_url, status,
			duration_ms, created_at, save_count)
		SELECT id, 1, '', '', '', 'published', 1000, '2026-11-20'::timestamptz + id * interval '1 second',
			CASE WHEN id BETWEEN 101 AND 200 THEN 1 ELSE 0 END
		FROM generate_series(1, 300) id;
		INSERT INTO products (id, title, brand, image_url, price, category)
			VALUES (1, 'Candle', 'INO', 'image', 800, 'favorite');
		INSERT INTO reel_products (reel_id, product_id, display_position)
			SELECT id, 1, 0 FROM generate_series(1, 100) id;`
	if _, err := pool.Exec(ctx, fixtures); err != nil {
		t.Fatal(err)
	}
	reader := postgres.NewFeedReader(pool)

	for _, tc := range []struct {
		name      string
		preferred []string
		wantFresh int
		wantLiked int
		wantOlder int
	}{
		{name: "public", wantFresh: 100, wantLiked: 100},
		{name: "personalized", preferred: []string{"favorite"}, wantFresh: 70, wantLiked: 70, wantOlder: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidates, err := reader.LoadFeedCandidates(ctx, "", tc.preferred)
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) != domain.MaxFeedCandidates {
				t.Fatalf("candidate count = %d, want %d", len(candidates), domain.MaxFeedCandidates)
			}
			var fresh, liked, older int
			ids := make(map[int64]bool, len(candidates))
			for _, candidate := range candidates {
				if ids[candidate.ID] {
					t.Fatalf("duplicate candidate ID %d", candidate.ID)
				}
				ids[candidate.ID] = true
				switch {
				case candidate.ID > 200:
					fresh++
				case candidate.ID > 100:
					liked++
				default:
					older++
				}
			}
			if fresh != tc.wantFresh || liked != tc.wantLiked || older != tc.wantOlder {
				t.Fatalf("candidate pools = [%d %d %d], want [%d %d %d]",
					fresh, liked, older, tc.wantFresh, tc.wantLiked, tc.wantOlder)
			}
		})
	}

	t.Run("overlapping fresh and popular buckets fill to the cap", func(t *testing.T) {
		if _, err := pool.Exec(ctx, "UPDATE reels SET save_count = 0"); err != nil {
			t.Fatal(err)
		}
		candidates, err := reader.LoadFeedCandidates(ctx, "", nil)
		if err != nil || len(candidates) != domain.MaxFeedCandidates {
			t.Fatalf("loaded %d candidates, err=%v", len(candidates), err)
		}
		seen := make(map[int64]bool, len(candidates))
		for _, candidate := range candidates {
			if candidate.ID <= 100 || candidate.ID > 300 || seen[candidate.ID] {
				t.Fatalf("unexpected or duplicate candidate %d", candidate.ID)
			}
			seen[candidate.ID] = true
		}
	})
}
