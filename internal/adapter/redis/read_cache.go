// Package redis adapts Redis to INO's PostgreSQL-backed read and receipt interfaces.
package redis

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"ino/internal/usecase"
)

const (
	cachePrefix     = "ino:cache:v1:"
	catalogEpochKey = cachePrefix + "catalog"
	fallbackTimeout = 750 * time.Millisecond
	productsTTL     = 15 * time.Second
	stateTTL        = 10 * time.Second
	identityTTL     = 15 * time.Second
	recentTTL       = 15 * time.Second
	receiptTTL      = 5 * time.Minute
)

type ReadCache struct {
	client    *goredis.Client
	timeout   time.Duration
	fallbacks chan struct{}
	loads     singleflight.Group
	logger    *slog.Logger
}

// client may be nil for a PostgreSQL-only deployment. The limiter still applies.
func NewReadCache(client *goredis.Client, timeout time.Duration, maxFallbacks int, logger *slog.Logger) *ReadCache {
	return &ReadCache{client: client, timeout: timeout, fallbacks: make(chan struct{}, maxFallbacks), logger: logger}
}

type cacheKey struct {
	name             string
	kind             string
	catalogDependent bool
}

func (key cacheKey) redisKeys() []string {
	if key.catalogDependent {
		return []string{key.name, catalogEpochKey}
	}
	return []string{key.name}
}

type cacheRead struct {
	generation string
	epoch      string
	value      string
}

// A miss reserves a generation before the PostgreSQL read starts. A catalog
// epoch invalidates both products and reel visibility without scanning keys.
var readScript = goredis.NewScript(`
local epoch = ''
if #KEYS == 2 then
    epoch = redis.call('GET', KEYS[2])
    if not epoch then
        epoch = ARGV[1]
        redis.call('SET', KEYS[2], epoch)
    end
end
local entry = redis.call('HMGET', KEYS[1], 'generation', 'epoch', 'value')
if not entry[1] or entry[2] ~= epoch then
    redis.call('DEL', KEYS[1])
    redis.call('HSET', KEYS[1], 'generation', ARGV[1], 'epoch', epoch)
    redis.call('PEXPIRE', KEYS[1], ARGV[2])
    return {ARGV[1], epoch, ''}
end
return {entry[1], epoch, entry[3] or ''}
`)

type fillOutcome int

const (
	fillRejected fillOutcome = iota
	fillInstalled
	fillAlreadyCurrent
)

var fillScript = goredis.NewScript(`
if #KEYS == 2 and redis.call('GET', KEYS[2]) ~= ARGV[2] then return 0 end
local entry = redis.call('HMGET', KEYS[1], 'generation', 'value')
if entry[1] ~= ARGV[1] then return 0 end
if entry[2] == ARGV[4] then return 2 end
if (entry[2] or '') ~= ARGV[3] then return 0 end
redis.call('HSET', KEYS[1], 'value', ARGV[4])
redis.call('PEXPIRE', KEYS[1], ARGV[5])
return 1
`)

func (cache *ReadCache) read(ctx context.Context, key cacheKey) (cacheRead, error) {
	ctx, cancel := context.WithTimeout(ctx, cache.timeout)
	defer cancel()
	parts, err := readScript.Run(ctx, cache.client, key.redisKeys(), rand.Text(), (5 * time.Second).Milliseconds()).StringSlice()
	if err != nil {
		return cacheRead{}, err
	}
	if len(parts) != 3 {
		return cacheRead{}, errors.New("unexpected cache read response")
	}
	return cacheRead{generation: parts[0], epoch: parts[1], value: parts[2]}, nil
}

func (cache *ReadCache) fill(ctx context.Context, key cacheKey, read cacheRead, value any, ttl time.Duration) (fillOutcome, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return fillRejected, err
	}
	ctx, cancel := context.WithTimeout(ctx, cache.timeout)
	defer cancel()
	// A matching value in the same generation is already current. The script
	// leaves both its value and expiration unchanged.
	outcome, err := fillScript.Run(ctx, cache.client, key.redisKeys(), read.generation, read.epoch, read.value, string(data), ttl.Milliseconds()).Int()
	return fillOutcome(outcome), err
}

func decodeCached[T any](value string, valid func(T) bool) (T, bool) {
	var result T
	if value == "" || json.Unmarshal([]byte(value), &result) != nil {
		return result, false
	}
	return result, valid(result)
}

func cachedRead[T any](ctx context.Context, cache *ReadCache, key cacheKey, ttl time.Duration, valid func(T) bool, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	result := cache.loads.DoChan(key.name, func() (any, error) {
		// One disconnected caller must not cancel the shared load. The work has
		// independent Redis and PostgreSQL deadlines and never waits for a slot.
		workCtx := context.WithoutCancel(ctx)
		var read cacheRead
		if cache.client != nil {
			read, _ = cache.read(workCtx, key)
			if value, ok := decodeCached(read.value, valid); ok {
				cache.logger.Debug("cache read", "kind", key.kind, "outcome", "hit")
				return value, nil
			}
		}
		cache.logger.Debug("cache read", "kind", key.kind, "outcome", "miss", "redis_available", read.generation != "")
		value, err := readPostgres(workCtx, cache, key.kind, load)
		if err != nil {
			return nil, err
		}
		if read.generation != "" {
			if _, err := cache.fill(workCtx, key, read, value, ttl); err != nil {
				cache.logger.Debug("cache fill failed", "kind", key.kind)
			}
		}
		return value, nil
	})
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case loaded := <-result:
		if loaded.Err != nil {
			return zero, loaded.Err
		}
		return loaded.Val.(T), nil
	}
}

func readPostgres[T any](ctx context.Context, cache *ReadCache, kind string, load func(context.Context) (T, error)) (T, error) {
	select {
	case cache.fallbacks <- struct{}{}:
		defer func() { <-cache.fallbacks }()
	default:
		cache.logger.Debug("cache fallback", "kind", kind, "outcome", "overloaded")
		var zero T
		return zero, usecase.ErrReadUnavailable
	}
	cache.logger.Debug("cache fallback", "kind", kind, "outcome", "postgres")
	ctx, cancel := context.WithTimeout(ctx, fallbackTimeout)
	defer cancel()
	value, err := load(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		var sqlError interface{ SQLState() string }
		// Preserve unexpected SQL failures as 500. Network, pool and deadline
		// failures have no PostgreSQL error code and surface as a controlled 503.
		if !errors.As(err, &sqlError) || unavailableSQLState(sqlError.SQLState()) {
			err = fmt.Errorf("%w: %w", usecase.ErrReadUnavailable, err)
		}
	}
	return value, err
}

func unavailableSQLState(code string) bool {
	return strings.HasPrefix(code, "08") || strings.HasPrefix(code, "53") ||
		code == "57P01" || code == "57P02" || code == "57P03"
}
