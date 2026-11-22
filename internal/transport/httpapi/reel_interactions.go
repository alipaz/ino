package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"ino/internal/usecase"
)

func RegisterReelInteractionRoutes(routes *gin.RouterGroup, logger *slog.Logger, interactions *usecase.ReelInteractions) {
	setInteraction := func(set func(context.Context, int64, int64, bool) error, active bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			request := c.MustGet(reelWriteRequestKey).(reelWriteRequest)
			if err := set(c.Request.Context(), request.userID, request.reelID, active); err != nil {
				writeReelError(c, logger, request, err)
				return
			}
			c.Status(http.StatusNoContent)
		}
	}
	routes.POST("/like", setInteraction(interactions.SetReelLike, true))
	routes.DELETE("/like", setInteraction(interactions.SetReelLike, false))
	routes.POST("/save", setInteraction(interactions.SetReelSave, true))
	routes.DELETE("/save", setInteraction(interactions.SetReelSave, false))
	routes.POST("/view", func(c *gin.Context) {
		request := c.MustGet(reelWriteRequestKey).(reelWriteRequest)
		if err := interactions.RecordDailyReelView(c.Request.Context(), request.userID, request.reelID); err != nil {
			writeReelError(c, logger, request, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
}

func writeReelError(c *gin.Context, logger *slog.Logger, request reelWriteRequest, err error) {
	switch {
	case errors.Is(err, usecase.ErrReelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "reel_not_found"})
	case errors.Is(err, usecase.ErrRecipientNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "recipient_not_found"})
	case errors.Is(err, usecase.ErrSelfShare):
		c.JSON(http.StatusBadRequest, gin.H{"error": "self_share"})
	case errors.Is(err, usecase.ErrShareKeyConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "idempotency_conflict"})
	case errors.Is(err, usecase.ErrPlaybackReelConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "playback_reel_conflict"})
	default:
		logger.Error("reel write failed", "path", c.FullPath(), "reel_id", request.reelID, "user_id", request.userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
	}
}
