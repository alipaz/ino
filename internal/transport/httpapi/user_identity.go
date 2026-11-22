package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type UserIdentityReader interface {
	UserExists(ctx context.Context, userID int64) (bool, error)
}

type reelWriteRequest struct {
	userID int64
	reelID int64
}

const reelWriteRequestKey = "ino.reelWriteRequest"

func NewReelWriteRoutes(router *gin.Engine, logger *slog.Logger, users UserIdentityReader) *gin.RouterGroup {
	return router.Group("/api/v1/reels/:id", func(c *gin.Context) {
		headers := c.Request.Header.Values("X-User-ID")
		if len(headers) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_user_identity"})
			return
		}
		userID, err := parsePositiveID(headers[0])
		if err != nil || userID == 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_user_identity"})
			return
		}
		exists, err := users.UserExists(c.Request.Context(), userID)
		if err != nil {
			logger.Error("check caller identity failed", "user_id", userID)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
			return
		}
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_user_identity"})
			return
		}

		reelID, err := parsePositiveID(c.Param("id"))
		if err != nil || reelID == 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_reel_id"})
			return
		}
		c.Set(reelWriteRequestKey, reelWriteRequest{userID: userID, reelID: reelID})
		c.Next()
	})
}

func parsePositiveID(value string) (int64, error) {
	id, err := strconv.ParseUint(value, 10, 63)
	return int64(id), err
}
