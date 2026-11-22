package redis

import (
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// NewClient configures Redis without dialing, so availability does not gate startup.
// An empty URL disables Redis.
func NewClient(url string, timeout time.Duration, maxConnections int) (*goredis.Client, error) {
	if url == "" {
		return nil, nil
	}
	options, err := goredis.ParseURL(url)
	if err != nil {
		return nil, errors.New("invalid REDIS_URL")
	}
	options.ContextTimeoutEnabled = true
	options.DialTimeout = timeout
	options.ReadTimeout = timeout
	options.WriteTimeout = timeout
	options.PoolTimeout = timeout
	options.MaxRetries = -1
	options.DialerRetries = 1
	options.DisableIdentity = true
	options.MaxActiveConns = maxConnections
	return goredis.NewClient(options), nil
}
