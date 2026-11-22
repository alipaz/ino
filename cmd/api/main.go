package main

import (
	"context"
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
	"ino/internal/config"
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
	reelProducts := usecase.NewReelProducts(postgres.NewReelProductReader(pool))
	httpapi.RegisterReelProductRoutes(router, logger, reelProducts)

	reelWriter := postgres.NewReelWriter(pool)
	reelWrites := httpapi.NewReelWriteRoutes(router, logger, reelWriter)
	httpapi.RegisterReelInteractionRoutes(reelWrites, logger, usecase.NewReelInteractions(reelWriter))
	httpapi.RegisterReelShareRoutes(reelWrites, logger, usecase.NewReelShares(reelWriter))
	httpapi.RegisterReelWatchSessionRoutes(reelWrites, logger, usecase.NewReelWatchSessions(reelWriter))
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
