package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ino/internal/usecase"
)

type productResponse struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Brand    string `json:"brand"`
	ImageURL string `json:"image_url"`
	Price    int64  `json:"price"`
	Category string `json:"category"`
}

func RegisterReelProductRoutes(router *gin.Engine, logger *slog.Logger, reelProducts *usecase.ReelProducts) {
	router.GET("/api/v1/reels/:id/products", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 63)
		if err != nil || id == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_reel_id"})
			return
		}

		products, err := reelProducts.ListReelProducts(c.Request.Context(), int64(id))
		switch {
		case errors.Is(err, usecase.ErrReelNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "reel_not_found"})
			return
		case err != nil:
			logger.Error("list reel products failed", "reel_id", id)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
			return
		}

		response := make([]productResponse, 0, len(products))
		for _, product := range products {
			response = append(response, productResponse(product))
		}
		c.JSON(http.StatusOK, gin.H{"products": response})
	})
}
