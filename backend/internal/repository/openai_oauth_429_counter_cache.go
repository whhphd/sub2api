package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	openAIOAuth429CounterPrefix = "openai_oauth_429_streak:account:"
	openAIOAuth429CounterTTL    = 35 * 24 * 60 * 60
)

var openAIOAuth429CounterIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local ttl = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, ttl)
	end

	return count
`)

type openAIOAuth429CounterCache struct {
	rdb *redis.Client
}

func NewOpenAIOAuth429CounterCache(rdb *redis.Client) service.OpenAIOAuth429CounterCache {
	return &openAIOAuth429CounterCache{rdb: rdb}
}

func (c *openAIOAuth429CounterCache) IncrementOpenAIOAuth429Count(ctx context.Context, accountID int64) (int64, error) {
	key := fmt.Sprintf("%s%d", openAIOAuth429CounterPrefix, accountID)
	count, err := openAIOAuth429CounterIncrScript.Run(ctx, c.rdb, []string{key}, openAIOAuth429CounterTTL).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment openai oauth 429 count: %w", err)
	}
	return count, nil
}

func (c *openAIOAuth429CounterCache) ResetOpenAIOAuth429Count(ctx context.Context, accountID int64) error {
	key := fmt.Sprintf("%s%d", openAIOAuth429CounterPrefix, accountID)
	return c.rdb.Del(ctx, key).Err()
}
