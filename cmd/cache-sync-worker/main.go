// Command cache-sync-worker projects committed PostgreSQL outbox work into Redis.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"ino/internal/adapter/postgres"
	cacheadapter "ino/internal/adapter/redis"
	"ino/internal/config"
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
		logger.Error("cache sync worker failed", "error", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	const (
		projectorDBConnections    = 2
		projectorRedisConnections = 2
		projectorSnapshotReads    = 1
	)
	redisClient, err := cacheadapter.NewClient(cfg.RedisURL, cfg.CacheRedisTimeout, projectorRedisConnections)
	if err != nil {
		return err
	}
	if redisClient == nil {
		logger.Info("cache sync worker disabled because REDIS_URL is empty")
		<-ctx.Done()
		return nil
	}
	defer redisClient.Close()

	startupCtx, cancel := context.WithTimeout(ctx, cfg.DBStartupTimeout)
	// An outbox transaction holds one connection while reading a snapshot on another.
	pool, err := postgres.OpenWithMaxConns(startupCtx, cfg.DatabaseURL, projectorDBConnections)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer pool.Close()

	readCache := cacheadapter.NewReadCache(redisClient, cfg.CacheRedisTimeout, projectorSnapshotReads, logger)
	outbox := postgres.NewCacheOutbox(pool)
	projector := cacheadapter.NewRecentInteractionProjector(readCache, postgres.NewInteractionReader(pool))
	logger.Info("cache sync worker started")
	projector.Run(ctx, outbox.ProcessBatch)
	logger.Info("cache sync worker stopped")
	return nil
}
