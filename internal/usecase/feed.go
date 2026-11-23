package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"ino/internal/domain"
)

const (
	DefaultFeedLimit = 20
	MaxFeedLimit     = 50
)

var (
	ErrInvalidFeedParameters = errors.New("invalid feed parameters")
	feedCategorySlug         = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

type FeedRanker interface {
	Preferences(signals domain.FeedSignals, now time.Time) domain.FeedPreferences
	Rank(candidates []domain.FeedCandidate, preferences domain.FeedPreferences, now time.Time) []int64
}

type FeedSource interface {
	LoadFeedSignals(ctx context.Context, viewerID int64) (domain.FeedSignals, error)
	LoadFeedCandidates(ctx context.Context, category string, preferred []string) ([]domain.FeedCandidate, error)
}

type FeedOrderCache interface {
	ReadFeedOrder(ctx context.Context, viewerID int64, category, rankingVersion string, load func(context.Context) ([]int64, error)) ([]int64, error)
}

type FeedPageReader interface {
	ReadFeedPage(ctx context.Context, category string, orderedIDs []int64, limit int) (domain.FeedPage, error)
}

type Feed struct {
	source  FeedSource
	ranker  FeedRanker
	orders  FeedOrderCache
	pages   FeedPageReader
	cursors *FeedCursor
}

func NewFeed(source FeedSource, ranker FeedRanker, orders FeedOrderCache, pages FeedPageReader, cursors *FeedCursor) *Feed {
	return &Feed{source: source, ranker: ranker, orders: orders, pages: pages, cursors: cursors}
}

type FeedRequest struct {
	ViewerID int64
	Category string
	Limit    int
	Cursor   string
}

type FeedResult struct {
	Items      []domain.FeedReel
	NextCursor *string
}

func (feed *Feed) Explore(ctx context.Context, request FeedRequest, now time.Time) (FeedResult, error) {
	if request.Limit == 0 {
		request.Limit = DefaultFeedLimit
	}
	if request.Limit < 1 || request.Limit > MaxFeedLimit || request.ViewerID < 0 || !validFeedCategory(request.Category) {
		return FeedResult{}, ErrInvalidFeedParameters
	}

	browse, err := feed.openBrowse(ctx, request, now)
	if err != nil {
		return FeedResult{}, err
	}
	remaining := browse.ids[browse.position:]
	if len(remaining) == 0 {
		return FeedResult{Items: []domain.FeedReel{}}, nil
	}

	page, err := feed.pages.ReadFeedPage(ctx, request.Category, remaining, request.Limit)
	if err != nil {
		return FeedResult{}, fmt.Errorf("materialize feed page: %w", err)
	}
	if page.Consumed <= 0 || page.Consumed < len(page.Items) || page.Consumed > len(remaining) ||
		len(page.Items) > request.Limit {
		return FeedResult{}, errors.New("feed page reader returned invalid positions")
	}
	if page.Items == nil {
		page.Items = []domain.FeedReel{}
	}
	result := FeedResult{Items: page.Items}
	browse.position += page.Consumed
	if browse.position < len(browse.ids) {
		token, err := feed.cursors.encode(browse)
		if err != nil {
			return FeedResult{}, fmt.Errorf("encode feed continuation: %w", err)
		}
		result.NextCursor = &token
	}
	return result, nil
}

func (feed *Feed) openBrowse(ctx context.Context, request FeedRequest, now time.Time) (feedBrowse, error) {
	if request.Cursor != "" {
		return feed.cursors.decode(request.Cursor, request.ViewerID, request.Category, now)
	}

	ids, err := feed.orders.ReadFeedOrder(ctx, request.ViewerID, request.Category, domain.FeedRankingVersion,
		func(ctx context.Context) ([]int64, error) {
			return feed.rankFeed(ctx, request.ViewerID, request.Category, now)
		})
	if err != nil {
		return feedBrowse{}, fmt.Errorf("read feed order: %w", err)
	}
	if !validFeedIDs(ids) {
		return feedBrowse{}, errors.New("feed ordering returned invalid reel IDs")
	}
	return feedBrowse{viewerID: request.ViewerID, category: request.Category, ids: ids, expiresAt: now.Add(feedCursorTTL)}, nil
}

func (feed *Feed) rankFeed(ctx context.Context, viewerID int64, category string, now time.Time) ([]int64, error) {
	var preferences domain.FeedPreferences
	if viewerID != 0 {
		signals, err := feed.source.LoadFeedSignals(ctx, viewerID)
		if err != nil {
			return nil, fmt.Errorf("load feed signals: %w", err)
		}
		preferences = feed.ranker.Preferences(signals, now)
	}

	candidates, err := feed.source.LoadFeedCandidates(ctx, category, preferences.StrongestCategories)
	if err != nil {
		return nil, fmt.Errorf("load feed candidates: %w", err)
	}
	if len(candidates) > domain.MaxFeedCandidates {
		return nil, errors.New("feed source exceeded candidate budget")
	}
	ids := feed.ranker.Rank(candidates, preferences, now)
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}

func validFeedCategory(category string) bool {
	return category == "" || (len(category) <= maxFeedCategoryLength && feedCategorySlug.MatchString(category))
}
