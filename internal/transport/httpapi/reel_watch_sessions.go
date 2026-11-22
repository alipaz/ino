package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"ino/internal/usecase"
)

func RegisterReelWatchSessionRoutes(routes *gin.RouterGroup, logger *slog.Logger, sessions *usecase.ReelWatchSessions) {
	routes.PUT("/watch-sessions/:playback_id", func(c *gin.Context) {
		playbackID := c.Param("playback_id")
		if !reelWriteToken.MatchString(playbackID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_playback_id"})
			return
		}
		var body struct {
			WatchedMilliseconds *int64 `json:"watched_ms"`
		}
		if !decodeReelWriteBody(c, &body) || body.WatchedMilliseconds == nil || *body.WatchedMilliseconds < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request_body"})
			return
		}

		request := c.MustGet(reelWriteRequestKey).(reelWriteRequest)
		progress, err := sessions.UpdateWatchProgress(c.Request.Context(), request.userID, request.reelID, playbackID, *body.WatchedMilliseconds)
		if err != nil {
			writeReelError(c, logger, request, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"watched_ms":       progress.WatchedMilliseconds,
			"watch_percentage": progress.WatchPercentage,
		})
	})
}
