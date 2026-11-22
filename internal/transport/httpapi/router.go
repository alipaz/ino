// Package httpapi adapts operational endpoints to Gin.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// NewRouter receives the PostgreSQL health operation without coupling HTTP to pgx.
func NewRouter(logger *slog.Logger, ping func(context.Context) error) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				// Do not log panic values, headers or query strings: they can contain secrets.
				logger.Error("http panic recovered", "method", c.Request.Method, "path", c.FullPath())
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"status": "internal_error"})
			}
			logger.Info("http request", "method", c.Request.Method, "path", c.FullPath(),
				"status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds())
		}()
		c.Next()
	})
	router.GET("/livez", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "alive"})
	})
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if err := ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	return router
}
