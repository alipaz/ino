package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"ino/internal/usecase"
)

type activityResponse struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	ReelID    int64     `json:"reel_id"`
	Type      string    `json:"type"`
	EventTime time.Time `json:"event_time"`
}

func RegisterInteractionReadRoutes(router *gin.Engine, logger *slog.Logger, users UserIdentityReader, reads *usecase.InteractionReads) {
	router.GET("/api/v1/reels/:id/interaction-state", func(c *gin.Context) {
		userID, ok := authenticateUser(c, logger, users)
		if !ok {
			return
		}
		reelID, err := parsePositiveID(c.Param("id"))
		if err != nil || reelID == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_reel_id"})
			return
		}

		state, err := reads.ReelState(c.Request.Context(), userID, reelID)
		if err != nil {
			writeInteractionReadError(c, logger, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"liked": state.Liked, "saved": state.Saved})
	})

	router.GET("/api/v1/me/interactions/recent", func(c *gin.Context) {
		userID, ok := authenticateUser(c, logger, users)
		if !ok {
			return
		}
		events, err := reads.Recent(c.Request.Context(), userID)
		if err != nil {
			writeInteractionReadError(c, logger, err)
			return
		}
		response := make([]activityResponse, 0, len(events))
		for _, event := range events {
			response = append(response, activityResponse{
				ID: event.ID, UserID: event.UserID, ReelID: event.ReelID,
				Type: event.Type, EventTime: event.Time.UTC(),
			})
		}
		c.JSON(http.StatusOK, gin.H{"events": response})
	})
}

func writeInteractionReadError(c *gin.Context, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, usecase.ErrReelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "reel_not_found"})
	case errors.Is(err, usecase.ErrReadUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "read_unavailable"})
	default:
		logger.Error("interaction read failed", "path", c.FullPath())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
	}
}
