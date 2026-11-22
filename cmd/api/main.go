package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"ino/internal/adapter/postgres"
	cacheadapter "ino/internal/adapter/redis"
	"ino/internal/config"
	"ino/internal/ranking"
	"ino/internal/transport/httpapi"
	"ino/internal/usecase"
)

func main() {
	os.Exit(start())
}

func start() int {
	var level slog.LevelVar
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &level}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		return 1
	}
	level.Set(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("application failed", "error", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	cursorSecret := []byte(cfg.FeedCursorSecret)
	if len(cursorSecret) == 0 {
		cursorSecret = make([]byte, 32)
		if _, err := rand.Read(cursorSecret); err != nil {
			return fmt.Errorf("generate feed cursor secret: %w", err)
		}
	}
	feedCursor, err := usecase.NewFeedCursor(cursorSecret)
	if err != nil {
		return err
	}

	startupCtx, cancel := context.WithTimeout(ctx, cfg.DBStartupTimeout)
	logger.Info("waiting for PostgreSQL")
	pool, err := postgres.Open(startupCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer pool.Close()

	gin.SetMode(gin.ReleaseMode)
	router := httpapi.NewRouter(logger, pool.Ping)
	redisClient, err := cacheadapter.NewClient(cfg.RedisURL, cfg.CacheRedisTimeout, 12)
	if err != nil {
		return err
	}
	if redisClient != nil {
		defer redisClient.Close()
	}
	readCache := cacheadapter.NewReadCache(redisClient, cfg.CacheRedisTimeout, 4, logger)
	productReader := cacheadapter.NewCachedReelProductReader(readCache, postgres.NewReelProductReader(pool))
	reelProducts := usecase.NewReelProducts(productReader)
	httpapi.RegisterReelProductRoutes(router, logger, reelProducts)

	reelWriter := postgres.NewReelWriter(pool)
	reelWrites := httpapi.NewReelWriteRoutes(router, logger, reelWriter)
	httpapi.RegisterReelInteractionRoutes(reelWrites, logger, usecase.NewReelInteractions(reelWriter))
	shareWriter := cacheadapter.NewCachedShareWriter(readCache, reelWriter)
	httpapi.RegisterReelShareRoutes(reelWrites, logger, usecase.NewReelShares(shareWriter))
	httpapi.RegisterReelWatchSessionRoutes(reelWrites, logger, usecase.NewReelWatchSessions(reelWriter))
	interactionReader := postgres.NewInteractionReader(pool)
	cachedInteractions := cacheadapter.NewCachedInteractionReader(readCache, interactionReader, interactionReader)
	cachedUsers := cacheadapter.NewCachedUserIdentityReader(readCache, reelWriter.UserExists)
	httpapi.RegisterInteractionReadRoutes(router, logger, cachedUsers, usecase.NewInteractionReads(cachedInteractions, cachedInteractions))

	feedSource := postgres.NewFeedReader(pool)
	cachedFeed := cacheadapter.NewCachedFeedReader(readCache, feedSource)
	feedRanker := ranking.NewWeightedFeedRanker(ranking.V1Policy())
	feed := usecase.NewFeed(feedSource, feedRanker, cachedFeed, cachedFeed, feedCursor)
	httpapi.RegisterFeedRoutes(router, logger, cachedUsers, feed)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	defer server.Close()
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.Info("HTTP server started", "address", listener.Addr().String())
	select {
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down HTTP server")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown HTTP: %w", err)
		}
		logger.Info("HTTP server stopped")
		return nil
	}
}
