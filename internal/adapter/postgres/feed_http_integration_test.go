package postgres_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"ino/internal/adapter/postgres"
	cache "ino/internal/adapter/redis"
	"ino/internal/ranking"
	"ino/internal/transport/httpapi"
	"ino/internal/usecase"
)

func TestFeedTraversesSeededPublishedReelsPostgres(t *testing.T) {
	pool := openCatalogTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if inserted, err := postgres.SeedAssessment(ctx, pool); err != nil || !inserted {
		t.Fatalf("seed: inserted=%v, err=%v", inserted, err)
	}
	var want int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM reels WHERE status = 'published'").Scan(&want); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	readCache := cache.NewReadCache(nil, time.Second, 4, logger)
	source := postgres.NewFeedReader(pool)
	cachedFeed := cache.NewCachedFeedReader(readCache, source)
	signer, err := usecase.NewFeedCursor([]byte("feed-integration-secret-only-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	feed := usecase.NewFeed(source, ranking.NewWeightedFeedRanker(ranking.V1Policy()), cachedFeed, cachedFeed, signer)
	router := httpapi.NewRouter(logger, pool.Ping)
	httpapi.RegisterFeedRoutes(router, logger, postgres.NewReelWriter(pool), feed)

	for _, viewer := range []struct{ name, header string }{{"guest", ""}, {"personalized", "1"}} {
		t.Run(viewer.name, func(t *testing.T) {
			const first = "/api/v1/feed?limit=50"
			path := first
			seen := make(map[int64]bool, want)
			for {
				page := getFeedResponse(t, router, path, viewer.header)
				for _, id := range page.reelIDs() {
					if seen[id] {
						t.Fatalf("duplicate reel %d", id)
					}
					seen[id] = true
				}
				if page.NextCursor == nil {
					break
				}
				if len(page.Items) == 0 || len(seen) >= want {
					t.Fatalf("unexpected continuation at %d results", len(seen))
				}
				path = first + "&cursor=" + url.QueryEscape(*page.NextCursor)
			}
			if len(seen) != want {
				t.Fatalf("feed returned %d unique published reels, want %d", len(seen), want)
			}
		})
	}
}

func TestFeedWarmReadsAndStableCursorPostgresRedis(t *testing.T) {
	ctx, pool, redisClient, activity := openPostgresRedisTest(t)
	const fixture = `
		UPDATE reels SET created_at = now() WHERE id = 1;
		INSERT INTO reels (id, creator_id, caption, video_url, thumbnail_url, status, created_at, duration_ms) VALUES
			(5, 1, 'archive next', 'video', 'thumbnail', 'published', now() - interval '12 hours', 30000),
			(6, 1, 'third', 'video', 'thumbnail', 'published', now() - interval '24 hours', 30000),
			(7, 1, 'fourth', 'video', 'thumbnail', 'published', now() - interval '36 hours', 30000);
		INSERT INTO products (id, title, brand, image_url, price, category) VALUES
			(1, 'Boots', 'INO', 'boots.png', 7999, 'outdoors'),
			(2, 'Shirt', 'INO', 'shirt.png', 2999, 'fashion');
		INSERT INTO reel_products (reel_id, product_id, display_position) VALUES
			(1, 1, 2), (1, 2, 0), (3, 1, 0), (5, 1, 0), (6, 1, 0), (7, 1, 0);`
	if _, err := pool.Exec(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	projector := cache.NewRecentInteractionProjector(newTestReadCache(redisClient), activity)
	drainCacheOutbox(t, ctx, pool, projector.Apply)

	queryCount := &cacheQueryCounter{}
	poolConfig := pool.Config()
	poolConfig.ConnConfig.Tracer = queryCount
	traced, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	requestRedis := newCacheRequestClient(t, redisClient)
	readCache := newTestReadCache(requestRedis)
	source := postgres.NewFeedReader(traced)
	cachedFeed := cache.NewCachedFeedReader(readCache, source)
	signer, err := usecase.NewFeedCursor([]byte("feed-integration-secret-only-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	feed := usecase.NewFeed(source, ranking.NewWeightedFeedRanker(ranking.V1Policy()), cachedFeed, cachedFeed, signer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpapi.NewRouter(logger, traced.Ping)
	identity := cache.NewCachedUserIdentityReader(readCache, postgres.NewReelWriter(traced).UserExists)
	httpapi.RegisterFeedRoutes(router, logger, identity, feed)

	const initial = "/api/v1/feed?category=outdoors&limit=1"
	first := getFeedResponse(t, router, initial, "1")
	if !reflect.DeepEqual(first.reelIDs(), []int64{1}) || first.NextCursor == nil {
		t.Fatalf("first page = %+v", first)
	}
	item := first.Items[0]
	if item.Creator.ID != 1 || item.Creator.DisplayName != "Creator" || len(item.Products) != 2 ||
		item.Products[0].Category != "fashion" || item.Products[1].Category != "outdoors" {
		t.Fatalf("creator or full product order mismatch: %+v", item)
	}
	if got := queryCount.n.Load(); got != 5 {
		t.Fatalf("cold authenticated GET issued %d PostgreSQL statements, expected five batched statements", got)
	}

	queryCount.n.Store(0)
	warm := getFeedResponse(t, router, initial, "1")
	if !reflect.DeepEqual(warm.reelIDs(), first.reelIDs()) || queryCount.n.Load() != 1 {
		t.Fatalf("warm feed = %+v, query count = %d", warm, queryCount.n.Load())
	}
	const public = "/api/v1/feed?category=outdoors"
	queryCount.n.Store(0)
	publicPage := getFeedResponse(t, router, public, "")
	if !reflect.DeepEqual(publicPage.reelIDs(), []int64{1, 5, 6, 7}) || publicPage.NextCursor != nil || queryCount.n.Load() != 2 {
		t.Fatalf("cold multi-reel public GET = %+v, PostgreSQL queries = %d, want two", publicPage, queryCount.n.Load())
	}
	queryCount.n.Store(0)
	if result := getFeedResponse(t, router, public, ""); !reflect.DeepEqual(result, publicPage) || queryCount.n.Load() != 1 {
		t.Fatalf("warm public GET = %+v, PostgreSQL queries = %d", result, queryCount.n.Load())
	}

	const empty = "/api/v1/feed?category=nonexistent&limit=1"
	emptyPage := getFeedResponse(t, router, empty, "1")
	if emptyPage.Items == nil || len(emptyPage.Items) != 0 || emptyPage.NextCursor != nil {
		t.Fatalf("empty feed shape = %+v", emptyPage)
	}
	queryCount.n.Store(0)
	_ = getFeedResponse(t, router, empty, "1")
	if got := queryCount.n.Load(); got != 0 {
		t.Fatalf("warm empty feed issued %d queries", got)
	}

	// Change both the eligible set and V1 engagement after starting the browse.
	const mutation = `
		UPDATE reels SET status = 'archived' WHERE id = 5;
		INSERT INTO reel_shares (sender_id, recipient_id, reel_id, idempotency_key) VALUES
			(1, 2, 7, 'feed-1'), (2, 1, 7, 'feed-2'), (3, 1, 7, 'feed-3');`
	if _, err := pool.Exec(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	t.Run("archival is visible before projection", func(t *testing.T) {
		queryCount.n.Store(0)
		got := getFeedResponse(t, router, public, "").reelIDs()
		if !reflect.DeepEqual(got, []int64{1, 6, 7}) || queryCount.n.Load() != 1 {
			t.Fatalf("warm page after archiving reel 5 = %v, PostgreSQL queries = %d", got, queryCount.n.Load())
		}
	})
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	reranked := getFeedResponse(t, router, initial, "1")
	if !reflect.DeepEqual(reranked.reelIDs(), []int64{7}) {
		t.Fatalf("a new browse should see reel 7 move first: %+v", reranked)
	}

	nextPath := initial + "&cursor=" + url.QueryEscape(*first.NextCursor)
	next := getFeedResponse(t, router, nextPath, "1")
	if !reflect.DeepEqual(next.reelIDs(), []int64{6}) || next.NextCursor == nil {
		t.Fatalf("continuation should skip archived reel 5 and retain reel 6 before reel 7: %+v", next)
	}
	queryCount.n.Store(0)
	if again := getFeedResponse(t, router, nextPath, "1"); !reflect.DeepEqual(again, next) || queryCount.n.Load() != 1 {
		t.Fatalf("warm continuation changed or exceeded one page query: %+v, queries=%d", again, queryCount.n.Load())
	}
	for _, tc := range []struct {
		path, user string
		status     int
	}{
		{nextPath, "2", http.StatusBadRequest},
		{"/api/v1/feed?category=fashion&limit=1&cursor=" + url.QueryEscape(*first.NextCursor), "1", http.StatusBadRequest},
		{initial, "missing", http.StatusUnauthorized},
		{initial, "999999", http.StatusUnauthorized},
	} {
		resp := serveFeedRequest(router, tc.path, tc.user)
		if resp.Code != tc.status {
			t.Fatalf("GET %s with user %q: status=%d body=%s", tc.path, tc.user, resp.Code, resp.Body)
		}
	}
	if err := requestRedis.Close(); err != nil {
		t.Fatal(err)
	}
	queryCount.n.Store(0)
	if outage := getFeedResponse(t, router, nextPath, "1"); !reflect.DeepEqual(outage, next) || queryCount.n.Load() != 2 {
		t.Fatalf("fallback changed cursor order or exceeded identity + one page query: %+v, queries=%d", outage, queryCount.n.Load())
	}
	last := getFeedResponse(t, router, initial+"&cursor="+url.QueryEscape(*next.NextCursor), "1")
	if !reflect.DeepEqual(last.reelIDs(), []int64{7}) || last.NextCursor != nil {
		t.Fatalf("last page = %+v", last)
	}

	traced.Close()
	if resp := serveFeedRequest(router, initial, ""); resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("feed without Redis or PostgreSQL should return 503: status=%d body=%s", resp.Code, resp.Body)
	}
}

type feedHTTPResponse struct {
	Items []struct {
		ID      int64 `json:"id"`
		Creator struct {
			ID          int64  `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"creator"`
		Products []struct {
			Category string `json:"category"`
		} `json:"products"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func (response feedHTTPResponse) reelIDs() []int64 {
	ids := make([]int64, 0, len(response.Items))
	for _, item := range response.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func serveFeedRequest(handler http.Handler, path, user string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		request.Header.Set("X-User-ID", user)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func getFeedResponse(t *testing.T, handler http.Handler, path, user string) feedHTTPResponse {
	t.Helper()
	response := serveFeedRequest(handler, path, user)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s", path, response.Code, response.Body)
	}
	var body feedHTTPResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}
