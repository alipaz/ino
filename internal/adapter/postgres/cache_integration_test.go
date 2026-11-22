package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"ino/internal/adapter/postgres"
	cache "ino/internal/adapter/redis"
	"ino/internal/domain"
	"ino/internal/transport/httpapi"
	"ino/internal/usecase"
)

func TestCachedGETsPostgresRedis(t *testing.T) {
	ctx, pool, redisClient, reads := openPostgresRedisTest(t)
	requestRedis := newCacheRequestClient(t, redisClient)
	handler, queryCount, disconnectPostgres := cachedTestRouter(t, ctx, pool, requestRedis)
	projector := cache.NewRecentInteractionProjector(newTestReadCache(redisClient), reads)
	drainCacheOutbox(t, ctx, pool, projector.Apply)

	requests := []struct {
		path string
		body string
	}{
		{"/api/v1/reels/1/products", `{"products":[]}`},
		{"/api/v1/reels/1/interaction-state", `{"liked":false,"saved":false}`},
		{"/api/v1/me/interactions/recent", `{"events":[]}`},
	}
	checkReads := func() {
		t.Helper()
		for _, request := range requests {
			assertCacheResponse(t, handler, http.MethodGet, request.path, "", "", http.StatusOK, request.body)
		}
	}
	checkReads()
	if got := queryCount.Load(); got != 4 {
		t.Fatalf("cold GETs issued %d PostgreSQL queries, want 4 (identity once, plus each read)", got)
	}

	queryCount.Store(0)
	checkReads()
	if got := queryCount.Load(); got != 0 {
		t.Fatalf("warm GETs issued %d PostgreSQL queries, want 0", got)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO products (id, title, brand, image_url, price, category)
		VALUES (1, 'New product', 'Brand', 'image.png', 100, 'category');
		INSERT INTO reel_products (reel_id, product_id, display_position) VALUES (1, 1, 0)
	`); err != nil {
		t.Fatal(err)
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	requests[0].body = `{"products":[{"id":1,"title":"New product","brand":"Brand","image_url":"image.png","price":100,"category":"category"}]}`
	checkReads()

	// Direct catalog edits must invalidate both cached product and state visibility.
	if _, err := pool.Exec(ctx, `UPDATE reels SET status = 'archived' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	for _, path := range []string{requests[0].path, requests[1].path} {
		assertCacheResponse(t, handler, http.MethodGet, path, "", "", http.StatusNotFound, `{"error":"reel_not_found"}`)
	}
	if _, err := pool.Exec(ctx, `UPDATE reels SET status = 'published' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	checkReads()

	if err := requestRedis.Close(); err != nil {
		t.Fatal(err)
	}
	queryCount.Store(0)
	checkReads()
	if got := queryCount.Load(); got != 5 {
		t.Fatalf("unavailable Redis GETs issued %d PostgreSQL queries, want 5", got)
	}

	disconnectPostgres()
	for _, request := range requests {
		assertCacheResponse(t, handler, http.MethodGet, request.path, "", "", http.StatusServiceUnavailable, `{"error":"read_unavailable"}`)
	}
}

func TestCachedShareReplaysPostgresRedis(t *testing.T) {
	ctx, pool, redisClient, _ := openPostgresRedisTest(t)
	requestRedis := newCacheRequestClient(t, redisClient)
	handler, queryCount, _ := cachedTestRouter(t, ctx, pool, requestRedis)

	const path = "/api/v1/reels/1/share"
	const payload = `{"recipient_user_id":2}`
	const key = "replay-after-archive"
	original := serveCacheRequest(handler, http.MethodPost, path, payload, key)
	if original.Code != http.StatusCreated || !strings.Contains(original.Body.String(), `"created_at":`) {
		t.Fatalf("create share: status %d, body %s", original.Code, original.Body)
	}
	if _, err := pool.Exec(ctx, `UPDATE reels SET status = 'archived' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	queryCount.Store(0)
	assertCacheResponse(t, handler, http.MethodPost, path, payload, key, http.StatusCreated, original.Body.String())
	assertCacheResponse(t, handler, http.MethodPost, path, `{"recipient_user_id":3}`, key, http.StatusConflict, `{"error":"idempotency_conflict"}`)
	if got := queryCount.Load(); got != 2 {
		t.Fatalf("warm share replay/conflict issued %d PostgreSQL queries, want 2 (authoritative identity only)", got)
	}

	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	queryCount.Store(0)
	assertCacheResponse(t, handler, http.MethodPost, path, payload, key, http.StatusCreated, original.Body.String())
	if queryCount.Load() <= 1 {
		t.Fatal("empty Redis replay did not consult PostgreSQL for the original share")
	}

	if err := requestRedis.Close(); err != nil {
		t.Fatal(err)
	}
	assertCacheResponse(t, handler, http.MethodPost, path, payload, key, http.StatusCreated, original.Body.String())
	assertCacheResponse(t, handler, http.MethodPost, path, payload, "new-key", http.StatusNotFound, `{"error":"reel_not_found"}`)
	assertCacheResponse(t, handler, http.MethodPost, path, `{"recipient_user_id":3}`, key, http.StatusConflict, `{"error":"idempotency_conflict"}`)
	for name, query := range map[string]string{
		"share receipts":   `SELECT count(*) FROM reel_shares`,
		"share activities": `SELECT count(*) FROM activity_events WHERE event_type = 'share_created'`,
	} {
		var count int
		if err := pool.QueryRow(ctx, query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s: count %d, err %v", name, count, err)
		}
	}
}

func TestRecentActivityProjectionPostgresRedis(t *testing.T) {
	ctx, pool, redisClient, reads := openPostgresRedisTest(t)
	readCache := newTestReadCache(redisClient)
	fallback := &countedRecentReader{source: reads}
	cached := cache.NewCachedInteractionReader(readCache, reads, fallback)
	projector := cache.NewRecentInteractionProjector(readCache, reads)
	outbox := postgres.NewCacheOutbox(pool)
	drainCacheOutbox(t, ctx, pool, projector.Apply)

	// Identical timestamps exercise the ID tie-breaker across the 100th item.
	if _, err := pool.Exec(ctx, `
		INSERT INTO activity_events (user_id, reel_id, event_type, event_time)
		SELECT 1, 1, 'save_added', '2025-01-02 00:00:00Z'::timestamptz
		FROM generate_series(1, 105);
		INSERT INTO cache_outbox (kind, user_id, reel_id) VALUES ('activity', 1, 1)
	`); err != nil {
		t.Fatal(err)
	}

	unavailableRedis := newCacheRequestClient(t, redisClient)
	if err := unavailableRedis.Close(); err != nil {
		t.Fatal(err)
	}
	unavailableProjector := cache.NewRecentInteractionProjector(newTestReadCache(unavailableRedis), reads)
	if applied, err := outbox.ProcessBatch(ctx, unavailableProjector.Apply); applied != 0 || err == nil {
		t.Fatalf("unavailable Redis batch: applied %d, err %v", applied, err)
	}

	interrupted := errors.New("simulated interruption after successful Redis write")
	var delayedBatch []domain.CacheChange
	applied, err := outbox.ProcessBatch(ctx, func(ctx context.Context, changes []domain.CacheChange) error {
		if err := projector.Apply(ctx, changes); err != nil {
			return err
		}
		delayedBatch = append(delayedBatch, changes...)
		return interrupted
	})
	if applied != 0 || !errors.Is(err, interrupted) || len(delayedBatch) != 1 {
		t.Fatalf("interrupted batch: applied %d, pending batch size %d, err %v", applied, len(delayedBatch), err)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cache_outbox`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("interrupted batch pending count %d, err %v", pending, err)
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	assertRecentSnapshot(t, ctx, cached, reads, 105, 6)

	// A later arrival with an older event time must not push out a newer event.
	if _, err := pool.Exec(ctx, `
		INSERT INTO activity_events (user_id, reel_id, event_type, event_time) VALUES
			(1, 1, 'save_added', '2025-01-01 00:00:00Z'),
			(1, 1, 'save_removed', '2025-01-02 00:00:00Z');
		INSERT INTO cache_outbox (kind, user_id, reel_id) VALUES ('activity', 1, 1)
	`); err != nil {
		t.Fatal(err)
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	if err := projector.Apply(ctx, delayedBatch); err != nil {
		t.Fatalf("replay already processed batch: %v", err)
	}
	assertRecentSnapshot(t, ctx, cached, reads, 107, 7)
	if fallback.calls != 0 {
		t.Fatalf("projected snapshot was not cached; fallbacks = %d", fallback.calls)
	}

	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	assertRecentSnapshot(t, ctx, cached, reads, 107, 7)
	assertRecentSnapshot(t, ctx, cached, reads, 107, 7)
	if fallback.calls != 1 {
		t.Fatalf("snapshot rebuild should read PostgreSQL once, got %d reads", fallback.calls)
	}
}

type countedRecentReader struct {
	source usecase.RecentInteractionReader
	calls  int
}

func (reader *countedRecentReader) ListRecentInteractions(ctx context.Context, userID int64) ([]domain.ActivityEvent, error) {
	reader.calls++
	return reader.source.ListRecentInteractions(ctx, userID)
}

func TestActivityTriggersPostgresRedis(t *testing.T) {
	ctx, pool, redisClient, reads := openPostgresRedisTest(t)
	readCache := newTestReadCache(redisClient)
	cached := cache.NewCachedInteractionReader(readCache, reads, reads)
	projector := cache.NewRecentInteractionProjector(readCache, reads)
	writer := postgres.NewReelWriter(pool)
	outbox := postgres.NewCacheOutbox(pool)
	drainCacheOutbox(t, ctx, pool, projector.Apply)

	assertSaved := func(want bool) {
		t.Helper()
		state, published, err := cached.ReadInteractionState(ctx, 1, 1)
		if err != nil || !published || state.Saved != want {
			t.Fatalf("saved state %+v, published %t, err %v; want saved=%t", state, published, err, want)
		}
	}
	assertSaved(false)
	var delayedSave []domain.CacheChange
	for range 2 {
		if err := writer.SetActiveReelInteraction(ctx, 1, 1, domain.Save, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := outbox.ProcessBatch(ctx, func(ctx context.Context, changes []domain.CacheChange) error {
		delayedSave = append(delayedSave, changes...)
		return projector.Apply(ctx, changes)
	}); err != nil {
		t.Fatal(err)
	}
	if len(delayedSave) == 0 {
		t.Fatal("save did not enqueue changes")
	}
	assertSaved(true)
	for range 2 {
		if err := writer.SetActiveReelInteraction(ctx, 1, 1, domain.Save, false); err != nil {
			t.Fatal(err)
		}
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	if err := projector.Apply(ctx, delayedSave); err != nil {
		t.Fatal(err)
	}
	assertSaved(false)

	// A failed reel counter update rolls back both activity and its outbox rows.
	if _, err := pool.Exec(ctx, `ALTER TABLE reels ADD CONSTRAINT test_no_likes CHECK (like_count = 0)`); err != nil {
		t.Fatal(err)
	}
	if err := writer.SetActiveReelInteraction(ctx, 1, 1, domain.Like, true); err == nil {
		t.Fatal("expected the counter constraint to reject the write")
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cache_outbox`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("rolled back interaction left %d outbox rows, err %v", pending, err)
	}

	if _, err := pool.Exec(ctx, `ALTER TABLE reels DROP CONSTRAINT test_no_likes`); err != nil {
		t.Fatal(err)
	}
	for _, active := range []bool{true, true, false, false} {
		if err := writer.SetActiveReelInteraction(ctx, 1, 1, domain.Like, active); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	for range 2 {
		if err := writer.RecordReelView(ctx, 1, 1, now); err != nil {
			t.Fatal(err)
		}
	}
	session := usecase.NewReelWatchSessions(writer)
	for _, report := range []struct{ watched, persisted int64 }{
		{20999, 20999}, {21000, 21000}, {21000, 21000}, {20500, 21000}, {22000, 22000},
	} {
		progress, err := session.UpdateWatchProgress(ctx, 1, 1, "interested-session", report.watched)
		if err != nil || progress.WatchedMilliseconds != report.persisted {
			t.Fatalf("update watch progress to %d: %+v, %v, want %d", report.watched, progress, err, report.persisted)
		}
		if report.watched == 20999 {
			var events int
			err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_events WHERE event_type = 'watch_interest'`).Scan(&events)
			if err != nil || events != 0 {
				t.Fatalf("watch activity before reaching 70%%: %d, err %v", events, err)
			}
		}
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	events, err := cached.ListRecentInteractions(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, event := range events {
		types = append(types, event.Type)
	}
	want := []string{"watch_interest", "view_counted", "like_removed", "like_added", "save_removed", "save_added"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("activity events = %v, want %v", types, want)
	}
}

func assertRecentSnapshot(t *testing.T, ctx context.Context, cached, source usecase.RecentInteractionReader, firstID, lastID int64) {
	t.Helper()
	got, err := cached.ListRecentInteractions(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	want, err := source.ListRecentInteractions(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 || got[0].ID != firstID || got[len(got)-1].ID != lastID || !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected Redis snapshot: got %d events, want 100 in PostgreSQL order from event %d to %d", len(got), firstID, lastID)
	}
}

func drainCacheOutbox(t *testing.T, ctx context.Context, pool *pgxpool.Pool, apply func(context.Context, []domain.CacheChange) error) {
	t.Helper()
	outbox := postgres.NewCacheOutbox(pool)
	for {
		count, err := outbox.ProcessBatch(ctx, apply)
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			return
		}
	}
}

func openPostgresRedisTest(t *testing.T) (context.Context, *pgxpool.Pool, *redis.Client, *postgres.InteractionReader) {
	t.Helper()
	dsn := os.Getenv("INO_TEST_REDIS_URL")
	if dsn == "" {
		t.Skip("INO_TEST_REDIS_URL must point to an empty, disposable Redis database")
	}
	ctx, pool, _ := openReelWriteTest(t)
	options, err := redis.ParseURL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	size, err := client.DBSize(ctx).Result()
	if err != nil || size != 0 {
		t.Fatalf("expected an empty, disposable Redis test database, size %d, err %v", size, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := client.FlushDB(ctx).Err(); err != nil {
			t.Errorf("clear disposable Redis DB: %v", err)
		}
	})
	return ctx, pool, client, postgres.NewInteractionReader(pool)
}

func newCacheRequestClient(t *testing.T, client *redis.Client) *redis.Client {
	t.Helper()
	opts := *client.Options()
	requestClient := redis.NewClient(&opts)
	t.Cleanup(func() { _ = requestClient.Close() })
	return requestClient
}

func newTestReadCache(client *redis.Client) *cache.ReadCache {
	return cache.NewReadCache(client, 150*time.Millisecond, 4, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type cacheQueryCounter struct{ n atomic.Uint64 }

func (counter *cacheQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	counter.n.Add(1)
	return ctx
}

func (*cacheQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func cachedTestRouter(t *testing.T, ctx context.Context, pool *pgxpool.Pool, client *redis.Client) (*gin.Engine, *atomic.Uint64, func()) {
	t.Helper()
	counter := &cacheQueryCounter{}
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)

	readCache := newTestReadCache(client)
	writer := postgres.NewReelWriter(traced)
	identity := cache.NewCachedUserIdentityReader(readCache, writer.UserExists)
	reader := postgres.NewInteractionReader(traced)
	cachedReader := cache.NewCachedInteractionReader(readCache, reader, reader)
	products := cache.NewCachedReelProductReader(readCache, postgres.NewReelProductReader(traced))
	shares := cache.NewCachedShareWriter(readCache, writer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpapi.NewRouter(logger, traced.Ping)
	httpapi.RegisterReelProductRoutes(router, logger, usecase.NewReelProducts(products))
	httpapi.RegisterInteractionReadRoutes(router, logger, identity, usecase.NewInteractionReads(cachedReader, cachedReader))
	writeRoutes := httpapi.NewReelWriteRoutes(router, logger, writer)
	httpapi.RegisterReelShareRoutes(writeRoutes, logger, usecase.NewReelShares(shares))
	return router, &counter.n, traced.Close
}

func serveCacheRequest(handler http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-User-ID", "1")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertCacheResponse(t *testing.T, handler http.Handler, method, path, body, key string, status int, want string) {
	t.Helper()
	response := serveCacheRequest(handler, method, path, body, key)
	if result := fmt.Sprintf("%d %s", response.Code, response.Body.String()); result != fmt.Sprintf("%d %s", status, want) {
		t.Fatalf("%s %s: got %s, want %d %s", method, path, result, status, want)
	}
}

func TestActivityArrivingDuringRebuildPostgresRedis(t *testing.T) {
	ctx, pool, redisClient, reads := openPostgresRedisTest(t)
	readCache := newTestReadCache(redisClient)
	projector := cache.NewRecentInteractionProjector(readCache, reads)
	writer := postgres.NewReelWriter(pool)
	if err := writer.SetActiveReelInteraction(ctx, 1, 1, domain.Like, true); err != nil {
		t.Fatal(err)
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}

	rebuilding := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	reader := &pausedRecentReader{source: reads, rebuilding: rebuilding, release: release}
	cached := cache.NewCachedInteractionReader(readCache, reads, reader)
	result := make(chan error, 1)
	go func() {
		_, err := cached.ListRecentInteractions(ctx, 1)
		result <- err
	}()
	select {
	case <-rebuilding:
	case err := <-result:
		t.Fatalf("initial rebuild never paused: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := writer.SetActiveReelInteraction(ctx, 1, 1, domain.Save, true); err != nil {
		t.Fatal(err)
	}
	drainCacheOutbox(t, ctx, pool, projector.Apply)
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	events, err := cached.ListRecentInteractions(ctx, 1)
	if err != nil || len(events) != 2 || events[0].Type != "save_added" || events[1].Type != "like_added" {
		t.Fatalf("recent after activity during rebuild = %+v, err = %v", events, err)
	}
	if reader.calls.Load() != 1 {
		t.Fatal("latest snapshot should already be complete in Redis")
	}
}

type pausedRecentReader struct {
	source     usecase.RecentInteractionReader
	rebuilding chan struct{}
	release    chan struct{}
	calls      atomic.Int64
}

func (reader *pausedRecentReader) ListRecentInteractions(ctx context.Context, userID int64) ([]domain.ActivityEvent, error) {
	events, err := reader.source.ListRecentInteractions(ctx, userID)
	if reader.calls.Add(1) == 1 && err == nil {
		close(reader.rebuilding)
		select {
		case <-reader.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return events, err
}

func TestProjectorForegroundFillPostgresRedis(t *testing.T) {
	for _, tc := range []struct {
		name             string
		changeGeneration bool
		addActivity      bool
		wantRetry        bool
	}{
		{name: "equivalent snapshot is acknowledged"},
		{name: "same snapshot in a different generation is rejected", changeGeneration: true, wantRetry: true},
		{name: "different snapshot in the same generation is rejected", addActivity: true, wantRetry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, pool, redisClient, reads := openPostgresRedisTest(t)
			readCache := newTestReadCache(redisClient)
			projector := cache.NewRecentInteractionProjector(readCache, reads)
			drainCacheOutbox(t, ctx, pool, projector.Apply)
			writer := postgres.NewReelWriter(pool)
			if err := writer.SetActiveReelInteraction(ctx, 1, 1, domain.Like, true); err != nil {
				t.Fatal(err)
			}

			paused := &pausedRecentReader{source: reads, rebuilding: make(chan struct{}), release: make(chan struct{})}
			outbox := postgres.NewCacheOutbox(pool)
			projector = cache.NewRecentInteractionProjector(readCache, paused)
			type outcome struct {
				applied int
				err     error
			}
			result := make(chan outcome, 1)
			go func() {
				applied, err := outbox.ProcessBatch(ctx, projector.Apply)
				result <- outcome{applied, err}
			}()
			defer func() {
				select {
				case <-paused.release:
				default:
					close(paused.release)
					<-result
				}
			}()
			select {
			case <-paused.rebuilding:
			case got := <-result:
				close(paused.release)
				t.Fatalf("projector did not pause: %+v", got)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}

			const recentKey = "ino:cache:v1:recent:1"
			if tc.changeGeneration {
				if err := redisClient.Del(ctx, recentKey).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.addActivity {
				if err := writer.SetActiveReelInteraction(ctx, 1, 1, domain.Save, true); err != nil {
					t.Fatal(err)
				}
			}
			handler, queries, _ := cachedTestRouter(t, ctx, pool, redisClient)
			const path = "/api/v1/me/interactions/recent"
			foreground := serveCacheRequest(handler, http.MethodGet, path, "", "")
			if foreground.Code != http.StatusOK || !strings.Contains(foreground.Body.String(), "like_added") || queries.Load() != 2 {
				t.Fatalf("foreground GET: status=%d, body=%s, queries=%d", foreground.Code, foreground.Body, queries.Load())
			}
			before, err := redisClient.HGetAll(ctx, recentKey).Result()
			if err != nil || before["value"] == "" {
				t.Fatalf("foreground did not install a Redis snapshot: %v, err=%v", before, err)
			}
			// Mark the foreground's TTL with an exact value so we detect a
			// projectile rewrite even if both fills finish in one millisecond.
			if err := redisClient.PExpire(ctx, recentKey, 10*time.Second).Err(); err != nil {
				t.Fatal(err)
			}
			expiration, err := redisClient.PExpireTime(ctx, recentKey).Result()
			if err != nil {
				t.Fatal(err)
			}
			close(paused.release)
			got := <-result
			if tc.wantRetry {
				if got.err == nil || got.applied != 0 {
					t.Fatalf("invalidated or different snapshot was acknowledged: %+v", got)
				}
			} else if got.err != nil || got.applied != 2 {
				t.Errorf("equivalent snapshot: applied %d, err = %v, want both outbox rows acknowledged", got.applied, got.err)
			}

			var pending int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM cache_outbox").Scan(&pending); err != nil {
				t.Fatal(err)
			}
			wantPending := 0
			if tc.wantRetry {
				wantPending = 2
				if tc.addActivity {
					wantPending += 2
				}
			}
			if pending != wantPending {
				t.Errorf("pending outbox rows = %d, want %d", pending, wantPending)
			}
			after, err := redisClient.HGetAll(ctx, recentKey).Result()
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("foreground snapshot changed: before=%+v, after=%+v, err=%v", before, after, err)
			}
			expirationAfter, err := redisClient.PExpireTime(ctx, recentKey).Result()
			if err != nil || expirationAfter != expiration {
				t.Fatalf("foreground TTL changed: before=%v, after=%v, err=%v", expiration, expirationAfter, err)
			}
			queries.Store(0)
			warm := serveCacheRequest(handler, http.MethodGet, path, "", "")
			if warm.Code != http.StatusOK || warm.Body.String() != foreground.Body.String() || queries.Load() != 0 {
				t.Fatalf("warm GET changed or required a rebuild: status=%d, body=%s, queries=%d", warm.Code, warm.Body, queries.Load())
			}
		})
	}
}
