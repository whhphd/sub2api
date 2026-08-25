package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const proxyHealthKeyPrefix = "proxy:health:"

func proxyHealthKey(proxyID int64) string { return fmt.Sprintf("%s%d", proxyHealthKeyPrefix, proxyID) }

var recordProxyFailureScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local threshold = tonumber(ARGV[3])
local cooldown = tonumber(ARGV[4])
local class = ARGV[5]
local message = ARGV[6]
local start = tonumber(redis.call('HGET', key, 'failure_window_start') or '0')
local count = tonumber(redis.call('HGET', key, 'consecutive_failures') or '0')
if start == 0 or now - start > window then
  start = now
  count = 0
end
count = count + 1
local open = tonumber(redis.call('HGET', key, 'open_until') or '0')
if count >= threshold and open < now + cooldown then
  open = now + cooldown
end
redis.call('HSET', key,
  'failure_window_start', start,
  'consecutive_failures', count,
  'last_failure_at', now,
  'last_failure_class', class,
  'last_error', message)
if open > 0 then redis.call('HSET', key, 'open_until', open) end
redis.call('EXPIRE', key, 7200)
return {count, start, open}
`)

type proxyHealthCache struct{ rdb *redis.Client }

func NewProxyHealthCache(rdb *redis.Client) service.ProxyHealthCache {
	return &proxyHealthCache{rdb: rdb}
}

func (c *proxyHealthCache) GetProxyHealth(ctx context.Context, proxyID int64) (*service.ProxyHealthState, error) {
	if c == nil || c.rdb == nil || proxyID <= 0 {
		return nil, nil
	}
	values, err := c.rdb.HMGet(ctx, proxyHealthKey(proxyID), "consecutive_failures", "failure_window_start", "open_until", "last_failure_at", "last_success_at", "last_failure_class", "last_error").Result()
	if err != nil {
		return nil, err
	}
	state := &service.ProxyHealthState{}
	state.ConsecutiveFailures = int(parseRedisInt(values[0]))
	state.FailureWindowStart = parseRedisTime(values[1])
	if open := parseRedisTime(values[2]); !open.IsZero() {
		state.OpenUntil = &open
	}
	state.LastFailureAt = parseRedisTime(values[3])
	state.LastSuccessAt = parseRedisTime(values[4])
	state.LastFailureClass = redisString(values[5])
	state.LastError = redisString(values[6])
	if state.ConsecutiveFailures == 0 && state.OpenUntil == nil && state.LastSuccessAt.IsZero() && state.LastFailureAt.IsZero() {
		return nil, nil
	}
	return state, nil
}

func (c *proxyHealthCache) RecordProxyFailure(ctx context.Context, proxyID int64, now time.Time, window time.Duration, threshold int, cooldown time.Duration, failureClass, message string) (*service.ProxyHealthState, error) {
	if c == nil || c.rdb == nil || proxyID <= 0 {
		return nil, nil
	}
	if threshold < 1 {
		threshold = 1
	}
	result, err := recordProxyFailureScript.Run(ctx, c.rdb, []string{proxyHealthKey(proxyID)}, now.Unix(), int64(window/time.Second), threshold, int64(cooldown/time.Second), failureClass, message).Result()
	if err != nil {
		return nil, err
	}
	values, ok := result.([]interface{})
	if !ok || len(values) < 3 {
		return nil, fmt.Errorf("unexpected proxy health script result %T", result)
	}
	state := &service.ProxyHealthState{ConsecutiveFailures: int(parseRedisInt(values[0])), FailureWindowStart: time.Unix(parseRedisInt(values[1]), 0), LastFailureAt: now, LastFailureClass: failureClass, LastError: message}
	if open := parseRedisInt(values[2]); open > 0 {
		t := time.Unix(open, 0)
		state.OpenUntil = &t
	}
	return state, nil
}

func (c *proxyHealthCache) RecordProxySuccess(ctx context.Context, proxyID int64, now time.Time) error {
	if c == nil || c.rdb == nil || proxyID <= 0 {
		return nil
	}
	key := proxyHealthKey(proxyID)
	_, err := c.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, key, "last_success_at", now.Unix(), "consecutive_failures", 0)
		pipe.HDel(ctx, key, "failure_window_start", "open_until", "last_failure_at", "last_failure_class", "last_error")
		pipe.Expire(ctx, key, 2*time.Hour)
		return nil
	})
	return err
}

func redisString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return ""
	}
}

func parseRedisInt(value interface{}) int64 {
	return func() int64 {
		n, _ := strconv.ParseInt(redisString(value), 10, 64)
		if n != 0 {
			return n
		}
		if f, ok := value.(int64); ok {
			return f
		}
		return 0
	}()
}

func parseRedisTime(value interface{}) time.Time {
	if n := parseRedisInt(value); n > 0 {
		return time.Unix(n, 0)
	}
	return time.Time{}
}
