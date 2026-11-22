package redis

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"ino/internal/usecase"
)

func TestFeedSharesFallbackBudgetWithIdentityRedis(t *testing.T) {
	readCache := openTestReadCache(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	feed := NewCachedFeedReader(readCache, nil)
	identity := NewCachedUserIdentityReader(readCache, func(context.Context, int64) (bool, error) {
		t.Error("identity lookup entered a saturated PostgreSQL budget")
		return true, nil
	})
	t.Cleanup(func() {
		_ = readCache.client.Del(context.Background(),
			cachePrefix+"feed:order:test:987:budget-test", identityKey(987).name, feedEpochKey).Err()
	})

	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := feed.ReadFeedOrder(ctx, 987, "budget-test", "test", func(ctx context.Context) ([]int64, error) {
			close(started)
			select {
			case <-release:
				return []int64{41}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("feed fallback failed to start: %v", err)
	case <-ctx.Done():
		close(release)
		<-done
		t.Fatal(ctx.Err())
	}

	_, err := identity.UserExists(ctx, 987)
	close(release)
	if loadErr := <-done; loadErr != nil {
		t.Fatal(loadErr)
	}
	if !errors.Is(err, usecase.ErrReadUnavailable) {
		t.Fatalf("identity while feed uses final slot: %v", err)
	}
	ids, err := feed.ReadFeedOrder(ctx, 987, "budget-test", "test", func(context.Context) ([]int64, error) {
		return nil, errors.New("warm feed should not call PostgreSQL")
	})
	if err != nil || !reflect.DeepEqual(ids, []int64{41}) {
		t.Fatalf("warm feed IDs = %v, err = %v", ids, err)
	}
}
