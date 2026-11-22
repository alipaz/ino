// Package postgres owns PostgreSQL connection setup and data access.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open waits for a successful query, bounded by the caller's startup context.
// The caller owns the returned pool and must close it after use.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return OpenWithMaxConns(ctx, dsn, 10)
}

// OpenWithMaxConns waits for PostgreSQL using a pool with an explicit connection budget.
func OpenWithMaxConns(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	cfg.MaxConns = maxConns
	cfg.ConnConfig.ConnectTimeout = 2 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("create PostgreSQL pool failed")
	}
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return pool, nil
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("PostgreSQL startup failed: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}
