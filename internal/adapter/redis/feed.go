package redis

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"ino/internal/domain"
)

const feedOrderTTL = 15 * time.Second

type FeedPageSource interface {
	LoadFeedPage(ctx context.Context, category string, orderedIDs []int64, limit int) (domain.FeedPage, error)
}

type CachedFeedReader struct {
	cache  *ReadCache
	source FeedPageSource
}

func NewCachedFeedReader(cache *ReadCache, source FeedPageSource) *CachedFeedReader {
	return &CachedFeedReader{cache: cache, source: source}
}

func (reader *CachedFeedReader) ReadFeedOrder(ctx context.Context, viewerID int64, category, rankingVersion string, load func(context.Context) ([]int64, error)) ([]int64, error) {
	key := cacheKey{
		name: fmt.Sprintf("%sfeed:order:%s:%d:%s", cachePrefix, rankingVersion, viewerID, category),
		kind: "feed_order", epochKey: catalogEpochKey,
	}
	if viewerID != 0 {
		key.scopeEpochKey = feedViewerEpochKey(viewerID)
	}
	return cachedRead(ctx, reader.cache, key, feedOrderTTL, validFeedOrder, func(ctx context.Context) ([]int64, error) {
		ids, err := load(ctx)
		if ids == nil {
			ids = []int64{}
		}
		return ids, err
	})
}

func feedViewerEpochKey(viewerID int64) string {
	return cachePrefix + "feed:viewer-epoch:" + strconv.FormatInt(viewerID, 10)
}

func validFeedOrder(ids []int64) bool {
	if ids == nil || len(ids) > domain.MaxFeedCandidates {
		return false
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; id <= 0 || duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func (reader *CachedFeedReader) ReadFeedPage(ctx context.Context, category string, orderedIDs []int64, limit int) (domain.FeedPage, error) {
	// The cached order can outlive a catalog change. The page query checks
	// publication and category against PostgreSQL for every response.
	return readPostgres(ctx, reader.cache, "feed_page", func(ctx context.Context) (domain.FeedPage, error) {
		return reader.source.LoadFeedPage(ctx, category, orderedIDs, limit)
	})
}
