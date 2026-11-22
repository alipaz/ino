package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ino/internal/domain"
	"ino/internal/usecase"
)

func TestFeedHTTPContract(t *testing.T) {
	cursors, err := usecase.NewFeedCursor([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, query string
		header      []string
		order       []int64
		readErr     error
		wantStatus  int
		wantBody    string
	}{
		{name: "public empty", wantStatus: 200, wantBody: `{"items":[],"next_cursor":null}`},
		{name: "published metadata with creator and products", query: "?category=outdoors", order: []int64{7}, wantStatus: 200,
			wantBody: `{"items":[{"id":7,"caption":"on the trail","video_url":"clip.mp4","thumbnail_url":"clip.png","created_at":"1970-01-01T00:00:20Z","duration_ms":1000,"like_count":12,"save_count":5,"view_count":99,"creator":{"id":3,"display_name":"Fern"},"products":[{"id":4,"title":"Bottle","brand":"Trail","image_url":"item.png","price":1250,"category":"outdoors"}]}],"next_cursor":null}`},
		{name: "private empty", header: []string{"1"}, query: "?limit=50", wantStatus: 200, wantBody: `{"items":[],"next_cursor":null}`},
		{name: "empty identity", header: []string{""}, wantStatus: 401, wantBody: `{"error":"invalid_user_identity"}`},
		{name: "malformed identity", header: []string{"-1"}, wantStatus: 401, wantBody: `{"error":"invalid_user_identity"}`},
		{name: "unknown identity", header: []string{"2"}, wantStatus: 401, wantBody: `{"error":"invalid_user_identity"}`},
		{name: "duplicate identity", header: []string{"1", "1"}, wantStatus: 401, wantBody: `{"error":"invalid_user_identity"}`},
		{name: "zero limit", query: "?limit=0", wantStatus: 400, wantBody: `{"error":"invalid_feed_parameters"}`},
		{name: "over limit", query: "?limit=51", wantStatus: 400, wantBody: `{"error":"invalid_feed_parameters"}`},
		{name: "duplicate filter", query: "?category=outdoors&category=camping", wantStatus: 400, wantBody: `{"error":"invalid_feed_parameters"}`},
		{name: "invalid slug", query: "?category=Outdoors", wantStatus: 400, wantBody: `{"error":"invalid_feed_parameters"}`},
		{name: "invalid cursor", query: "?cursor=bad", wantStatus: 400, wantBody: `{"error":"invalid_cursor"}`},
		{name: "fallback unavailable", readErr: usecase.ErrReadUnavailable, wantStatus: 503, wantBody: `{"error":"read_unavailable"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := feedHTTPStub{order: tc.order, readErr: tc.readErr}
			feed := usecase.NewFeed(stub, stub, stub, stub, cursors)
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			router := NewRouter(logger, func(context.Context) error { return nil })
			RegisterFeedRoutes(router, logger, stub, feed)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/feed"+tc.query, nil)
			for _, value := range tc.header {
				request.Header.Add("X-User-ID", value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.wantStatus || !sameReelResponse(response.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d %s", response.Code, response.Body, tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestFeedHTTPRejectsExpiredCursorWithGone(t *testing.T) {
	cursors, err := usecase.NewFeedCursor([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	stub := feedHTTPStub{order: []int64{7, 8}}
	feed := usecase.NewFeed(stub, stub, stub, stub, cursors)
	first, err := feed.Explore(context.Background(), usecase.FeedRequest{Limit: 1}, time.Now().Add(-11*time.Minute))
	if err != nil || first.NextCursor == nil {
		t.Fatalf("could not prepare expired cursor: %+v, %v", first, err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := NewRouter(logger, func(context.Context) error { return nil })
	RegisterFeedRoutes(router, logger, stub, feed)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/feed?cursor="+*first.NextCursor, nil))
	if response.Code != 410 || !sameReelResponse(response.Body.String(), `{"error":"expired_cursor"}`) {
		t.Fatalf("unexpected response %d %s", response.Code, response.Body)
	}
}

type feedHTTPStub struct {
	order   []int64
	readErr error
}

func (feedHTTPStub) UserExists(_ context.Context, userID int64) (bool, error) {
	return userID == 1, nil
}
func (feedHTTPStub) LoadFeedSignals(context.Context, int64) (domain.FeedSignals, error) {
	return domain.FeedSignals{}, nil
}
func (feedHTTPStub) LoadFeedCandidates(context.Context, string, []string) ([]domain.FeedCandidate, error) {
	return nil, nil
}
func (feedHTTPStub) Preferences(domain.FeedSignals, time.Time) domain.FeedPreferences {
	return domain.FeedPreferences{}
}
func (stub feedHTTPStub) Rank([]domain.FeedCandidate, domain.FeedPreferences, time.Time) []int64 {
	return stub.order
}
func (stub feedHTTPStub) ReadFeedOrder(ctx context.Context, _ int64, _, _ string, load func(context.Context) ([]int64, error)) ([]int64, error) {
	if stub.readErr != nil {
		return nil, stub.readErr
	}
	return load(ctx)
}
func (feedHTTPStub) ReadFeedPage(_ context.Context, _ int64, _, _ string, ids []int64, limit int) (domain.FeedPage, error) {
	items := make([]domain.FeedReel, 0, limit)
	for _, id := range ids[:min(len(ids), limit)] {
		items = append(items, domain.FeedReel{
			ID: id, Caption: "on the trail", VideoURL: "clip.mp4", ThumbnailURL: "clip.png",
			CreatedAt: time.Unix(20, 0), DurationMS: 1000, LikeCount: 12, SaveCount: 5, ViewCount: 99,
			CreatorID: 3, CreatorName: "Fern",
			Products: []domain.Product{{ID: 4, Title: "Bottle", Brand: "Trail", ImageURL: "item.png", Price: 1250, Category: "outdoors"}},
		})
	}
	return domain.FeedPage{Items: items, Consumed: len(items)}, nil
}
