package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"ino/internal/usecase"
)

type feedCreatorResponse struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
}

type feedItemResponse struct {
	ID           int64               `json:"id"`
	Caption      string              `json:"caption"`
	VideoURL     string              `json:"video_url"`
	ThumbnailURL string              `json:"thumbnail_url"`
	CreatedAt    time.Time           `json:"created_at"`
	DurationMS   int64               `json:"duration_ms"`
	LikeCount    int64               `json:"like_count"`
	SaveCount    int64               `json:"save_count"`
	ViewCount    int64               `json:"view_count"`
	Creator      feedCreatorResponse `json:"creator"`
	Products     []productResponse   `json:"products"`
}

func RegisterFeedRoutes(router *gin.Engine, logger *slog.Logger, users UserIdentityReader, feed *usecase.Feed) {
	router.GET("/api/v1/feed", func(c *gin.Context) {
		request, err := parseFeedQuery(c.Request.URL.RawQuery)
		if err != nil {
			writeFeedError(c, logger, err)
			return
		}
		if len(c.Request.Header.Values("X-User-ID")) > 0 {
			viewerID, ok := authenticateUser(c, logger, users)
			if !ok {
				return
			}
			request.ViewerID = viewerID
		}

		result, err := feed.Explore(c.Request.Context(), request, time.Now())
		if err != nil {
			writeFeedError(c, logger, err)
			return
		}
		items := make([]feedItemResponse, 0, len(result.Items))
		for _, reel := range result.Items {
			products := make([]productResponse, 0, len(reel.Products))
			for _, product := range reel.Products {
				products = append(products, productResponse(product))
			}
			items = append(items, feedItemResponse{
				ID: reel.ID, Caption: reel.Caption, VideoURL: reel.VideoURL, ThumbnailURL: reel.ThumbnailURL,
				CreatedAt: reel.CreatedAt.UTC(), DurationMS: reel.DurationMS,
				LikeCount: reel.LikeCount, SaveCount: reel.SaveCount, ViewCount: reel.ViewCount,
				Creator:  feedCreatorResponse{ID: reel.CreatorID, DisplayName: reel.CreatorName},
				Products: products,
			})
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "next_cursor": result.NextCursor})
	})
}

func parseFeedQuery(raw string) (usecase.FeedRequest, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return usecase.FeedRequest{}, usecase.ErrInvalidFeedParameters
	}
	request := usecase.FeedRequest{Limit: usecase.DefaultFeedLimit}
	for name, values := range query {
		if len(values) != 1 || values[0] == "" {
			return usecase.FeedRequest{}, usecase.ErrInvalidFeedParameters
		}
		switch name {
		case "category":
			request.Category = values[0]
		case "limit":
			limit, err := strconv.ParseUint(values[0], 10, 8)
			if err != nil || limit == 0 || limit > usecase.MaxFeedLimit {
				return usecase.FeedRequest{}, usecase.ErrInvalidFeedParameters
			}
			request.Limit = int(limit)
		case "cursor":
			request.Cursor = values[0]
		default:
			return usecase.FeedRequest{}, usecase.ErrInvalidFeedParameters
		}
	}
	return request, nil
}

func writeFeedError(c *gin.Context, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, usecase.ErrInvalidFeedParameters):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_feed_parameters"})
	case errors.Is(err, usecase.ErrInvalidFeedCursor):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cursor"})
	case errors.Is(err, usecase.ErrExpiredFeedCursor):
		c.JSON(http.StatusGone, gin.H{"error": "expired_cursor"})
	case errors.Is(err, usecase.ErrReadUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "read_unavailable"})
	default:
		logger.Error("feed read failed", "path", c.FullPath())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
	}
}
