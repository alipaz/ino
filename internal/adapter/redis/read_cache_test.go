package redis

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"ino/internal/usecase"
)

func openTestReadCache(t *testing.T, slots int) *ReadCache {
	t.Helper()
	url := os.Getenv("INO_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set INO_TEST_REDIS_URL for Redis integration")
	}
	options, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := goredis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	return NewReadCache(client, 250*time.Millisecond, slots, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestPostgresReadWaitsForBrieflyBusySlot(t *testing.T) {
	cache := NewReadCache(nil, time.Second, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cache.fallbacks <- struct{}{}

	result := make(chan error, 1)
	go func() {
		_, err := readPostgres(ctx, cache, "test", func(context.Context) (int, error) {
			return 42, nil
		})
		result <- err
	}()
	waitForQueuedRead(t, cache, result)
	<-cache.fallbacks
	if err := <-result; err != nil {
		t.Fatalf("read after releasing a PostgreSQL slot: %v", err)
	}
}

func TestPostgresReadCancelsWhileWaiting(t *testing.T) {
	cache := NewReadCache(nil, time.Second, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cache.fallbacks <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan error, 1)
	go func() {
		_, err := readPostgres(ctx, cache, "test", func(context.Context) (int, error) {
			t.Error("canceled read ran a query")
			return 0, nil
		})
		result <- err
	}()
	waitForQueuedRead(t, cache, result)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	if len(cache.waiters) != 0 || len(cache.fallbacks) != 1 {
		t.Fatal("canceled read did not release its waiting slot or changed the active budget")
	}
}

func TestPostgresReadRejectsFullQueue(t *testing.T) {
	cache := NewReadCache(nil, time.Second, 4, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if cap(cache.waiters) != 16 {
		t.Fatalf("read queue capacity = %d, want 16", cap(cache.waiters))
	}
	for range cap(cache.fallbacks) {
		cache.fallbacks <- struct{}{}
	}
	for range cap(cache.waiters) {
		cache.waiters <- struct{}{}
	}
	_, err := readPostgres(context.Background(), cache, "test", func(context.Context) (int, error) {
		t.Fatal("read ran with all active and waiting slots occupied")
		return 0, nil
	})
	if !errors.Is(err, usecase.ErrReadUnavailable) {
		t.Fatalf("read with a full queue: %v", err)
	}
}

func waitForQueuedRead(t *testing.T, cache *ReadCache, result <-chan error) {
	t.Helper()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for len(cache.waiters) == 0 {
		select {
		case err := <-result:
			t.Fatalf("read finished before waiting for a slot: %v", err)
		case <-timeout.C:
			t.Fatal("read never waited for a slot")
		case <-poll.C:
		}
	}
}

func TestCoalescesSameKeyAndBoundsFallbacksRedis(t *testing.T) {
	cache := openTestReadCache(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := cacheKey{name: cachePrefix + "test:coalesce", kind: "test"}
	other := cacheKey{name: cachePrefix + "test:overload", kind: "test"}
	t.Cleanup(func() { _ = cache.client.Del(context.Background(), key.name, other.name).Err() })

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	load := func(ctx context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return 24, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	valid := func(value int) bool { return value == 24 }

	var readers sync.WaitGroup
	for range 16 {
		readers.Go(func() {
			value, err := cachedRead(ctx, cache, key, time.Minute, valid, load)
			if err != nil || value != 24 {
				t.Errorf("cached read = %d, err = %v", value, err)
			}
		})
	}
	select {
	case <-started:
	case <-ctx.Done():
		close(release)
		readers.Wait()
		t.Fatal("fallback did not start")
	}
	_, err := cachedRead(ctx, cache, other, time.Minute, valid, load)
	close(release)
	readers.Wait()
	if !errors.Is(err, usecase.ErrReadUnavailable) {
		t.Fatalf("overloaded read = %v, expected controlled unavailability", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("PostgreSQL loads = %d, want one", got)
	}
}

func TestOlderFillCannotRestoreInvalidatedEntryRedis(t *testing.T) {
	cache := openTestReadCache(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range []cacheKey{
		{name: cachePrefix + "test:user-state", kind: "test"},
		{name: cachePrefix + "test:catalog", kind: "test", epochKey: catalogEpochKey},
	} {
		t.Run(key.name, func(t *testing.T) {
			invalidation := key.name
			if key.epochKey != "" {
				invalidation = key.epochKey
			}
			t.Cleanup(func() { _ = cache.client.Del(context.Background(), key.name, invalidation).Err() })
			readBeforeWrite, err := cache.read(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if err := cache.client.Del(ctx, invalidation).Err(); err != nil {
				t.Fatal(err)
			}
			readAfterWrite, err := cache.read(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if outcome, err := cache.fill(ctx, key, readAfterWrite, false, time.Minute); err != nil || outcome != fillInstalled {
				t.Fatalf("new fill outcome = %v, err = %v", outcome, err)
			}
			if outcome, err := cache.fill(ctx, key, readBeforeWrite, true, time.Minute); err != nil || outcome != fillRejected {
				t.Fatalf("old fill outcome = %v, err = %v", outcome, err)
			}
			current, err := cache.read(ctx, key)
			if err != nil || current.value != "false" {
				t.Fatalf("old fill replaced current value: %+v, err = %v", current, err)
			}
			if err := cache.client.Del(ctx, key.name, invalidation).Err(); err != nil {
				t.Fatal(err)
			}
			if outcome, err := cache.fill(ctx, key, readAfterWrite, true, time.Minute); err != nil || outcome != fillRejected {
				t.Fatalf("pre-loss fill outcome = %v, err = %v", outcome, err)
			}
			current, err = cache.read(ctx, key)
			if err != nil || current.value != "" {
				t.Fatalf("pre-loss fill repopulated reset key: %+v, err = %v", current, err)
			}
		})
	}
}
