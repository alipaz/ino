package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"ino/internal/usecase"
)

var reelWriteToken = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

type shareResponse struct {
	ID             int64     `json:"id"`
	SenderID       int64     `json:"sender_id"`
	RecipientID    int64     `json:"recipient_id"`
	ReelID         int64     `json:"reel_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
}

func RegisterReelShareRoutes(routes *gin.RouterGroup, logger *slog.Logger, shares *usecase.ReelShares) {
	routes.POST("/share", func(c *gin.Context) {
		keys := c.Request.Header.Values("Idempotency-Key")
		if len(keys) != 1 || !reelWriteToken.MatchString(keys[0]) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_idempotency_key"})
			return
		}
		var body struct {
			RecipientID int64 `json:"recipient_user_id"`
		}
		if !decodeReelWriteBody(c, &body) || body.RecipientID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request_body"})
			return
		}

		request := c.MustGet(reelWriteRequestKey).(reelWriteRequest)
		share, err := shares.ShareReel(c.Request.Context(), request.userID, request.reelID, body.RecipientID, keys[0])
		if err != nil {
			writeReelError(c, logger, request, err)
			return
		}
		c.JSON(http.StatusCreated, shareResponse{
			ID: share.ID, SenderID: share.SenderID, RecipientID: share.RecipientID,
			ReelID: share.ReelID, IdempotencyKey: share.IdempotencyKey, CreatedAt: share.CreatedAt.UTC(),
		})
	})
}

func decodeReelWriteBody(c *gin.Context, body any) bool {
	const maxBodyBytes = 2048
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	return decoder.Decode(body) == nil && decoder.Decode(new(json.RawMessage)) == io.EOF
}
