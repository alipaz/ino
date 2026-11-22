package redis

import (
	"context"
	"strconv"

	"ino/internal/domain"
	"ino/internal/usecase"
)

func productKey(reelID int64) cacheKey {
	return cacheKey{cachePrefix + "products:" + strconv.FormatInt(reelID, 10), "products", catalogEpochKey}
}

func interactionKey(userID, reelID int64) cacheKey {
	return cacheKey{cachePrefix + "state:" + strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(reelID, 10), "interaction_state", catalogEpochKey}
}

func recentKey(userID int64) cacheKey {
	return cacheKey{cachePrefix + "recent:" + strconv.FormatInt(userID, 10), "recent_activity", ""}
}

func identityKey(userID int64) cacheKey {
	return cacheKey{cachePrefix + "identity:" + strconv.FormatInt(userID, 10), "identity", ""}
}

type CachedReelProductReader struct {
	cache  *ReadCache
	source usecase.ReelProductReader
}

func NewCachedReelProductReader(cache *ReadCache, source usecase.ReelProductReader) *CachedReelProductReader {
	return &CachedReelProductReader{cache: cache, source: source}
}

type productSnapshot struct {
	Published *bool            `json:"published"`
	Products  []domain.Product `json:"products"`
}

func (reader *CachedReelProductReader) ListPublishedReelProducts(ctx context.Context, reelID int64) ([]domain.Product, bool, error) {
	snapshot, err := cachedRead(ctx, reader.cache, productKey(reelID), productsTTL,
		func(snapshot productSnapshot) bool { return snapshot.Published != nil && snapshot.Products != nil },
		func(ctx context.Context) (productSnapshot, error) {
			products, published, err := reader.source.ListPublishedReelProducts(ctx, reelID)
			if products == nil {
				products = []domain.Product{}
			}
			return productSnapshot{Published: &published, Products: products}, err
		})
	if err != nil {
		return nil, false, err
	}
	return snapshot.Products, *snapshot.Published, nil
}

type CachedInteractionReader struct {
	cache  *ReadCache
	state  usecase.InteractionStateReader
	recent usecase.RecentInteractionReader
}

func NewCachedInteractionReader(cache *ReadCache, state usecase.InteractionStateReader, recent usecase.RecentInteractionReader) *CachedInteractionReader {
	return &CachedInteractionReader{cache: cache, state: state, recent: recent}
}

type interactionSnapshot struct {
	Published *bool `json:"published"`
	Liked     *bool `json:"liked"`
	Saved     *bool `json:"saved"`
}

func (reader *CachedInteractionReader) ReadInteractionState(ctx context.Context, userID, reelID int64) (domain.InteractionState, bool, error) {
	snapshot, err := cachedRead(ctx, reader.cache, interactionKey(userID, reelID), stateTTL,
		func(snapshot interactionSnapshot) bool {
			return snapshot.Published != nil && snapshot.Liked != nil && snapshot.Saved != nil
		},
		func(ctx context.Context) (interactionSnapshot, error) {
			state, published, err := reader.state.ReadInteractionState(ctx, userID, reelID)
			return interactionSnapshot{Published: &published, Liked: &state.Liked, Saved: &state.Saved}, err
		})
	if err != nil {
		return domain.InteractionState{}, false, err
	}
	return domain.InteractionState{Liked: *snapshot.Liked, Saved: *snapshot.Saved}, *snapshot.Published, nil
}

type activitySnapshot struct {
	Complete bool                   `json:"complete"`
	Events   []domain.ActivityEvent `json:"events"`
}

func loadActivity(ctx context.Context, source usecase.RecentInteractionReader, userID int64) (activitySnapshot, error) {
	events, err := source.ListRecentInteractions(ctx, userID)
	if events == nil {
		events = []domain.ActivityEvent{}
	}
	return activitySnapshot{Complete: true, Events: events}, err
}

func (reader *CachedInteractionReader) ListRecentInteractions(ctx context.Context, userID int64) ([]domain.ActivityEvent, error) {
	snapshot, err := cachedRead(ctx, reader.cache, recentKey(userID), recentTTL,
		func(snapshot activitySnapshot) bool {
			return snapshot.Complete && snapshot.Events != nil && len(snapshot.Events) <= 100
		},
		func(ctx context.Context) (activitySnapshot, error) { return loadActivity(ctx, reader.recent, userID) })
	return snapshot.Events, err
}

type CachedUserIdentityReader struct {
	cache  *ReadCache
	exists func(context.Context, int64) (bool, error)
}

func NewCachedUserIdentityReader(cache *ReadCache, exists func(context.Context, int64) (bool, error)) *CachedUserIdentityReader {
	return &CachedUserIdentityReader{cache: cache, exists: exists}
}

func (reader *CachedUserIdentityReader) UserExists(ctx context.Context, userID int64) (bool, error) {
	exists, err := cachedRead(ctx, reader.cache, identityKey(userID), identityTTL,
		func(exists *bool) bool { return exists != nil },
		func(ctx context.Context) (*bool, error) {
			exists, err := reader.exists(ctx, userID)
			return &exists, err
		})
	if err != nil {
		return false, err
	}
	return *exists, nil
}
