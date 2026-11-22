package redis

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"

	"ino/internal/domain"
)

const (
	feedOrderTTL = 15 * time.Second
	feedPageTTL  = 10 * time.Second
)

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

func (reader *CachedFeedReader) ReadFeedPage(ctx context.Context, viewerID int64, category, rankingVersion string, orderedIDs []int64, limit int) (domain.FeedPage, error) {
	encodedIDs := make([]byte, 0, len(orderedIDs)*8)
	for _, id := range orderedIDs {
		encodedIDs = binary.BigEndian.AppendUint64(encodedIDs, uint64(id))
	}
	digest := sha256.Sum256(encodedIDs)
	key := cacheKey{
		name: fmt.Sprintf("%sfeed:page:%s:%d:%s:%d:%x", cachePrefix, rankingVersion, viewerID, category, limit, digest),
		kind: "feed_page", epochKey: catalogEpochKey,
	}
	return cachedRead(ctx, reader.cache, key, feedPageTTL, func(page domain.FeedPage) bool {
		return page.Items != nil && len(page.Items) <= limit && page.Consumed >= len(page.Items) &&
			page.Consumed <= len(orderedIDs) && (page.Consumed == len(orderedIDs) || len(page.Items) == limit)
	}, func(ctx context.Context) (domain.FeedPage, error) {
		page, err := reader.source.LoadFeedPage(ctx, category, orderedIDs, limit)
		if page.Items == nil {
			page.Items = []domain.FeedReel{}
		}
		return page, err
	})
}
