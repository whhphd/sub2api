package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	openAIOAuth429CounterPrefix       = "openai_oauth_429_window:account:"
	openAIOAuth429CounterGraceSeconds = 60
	legacyOpenAIOAuth429CounterPrefix = "openai_oauth_429_streak:account:"
	legacyOpenAIOAuth429CounterTTL    = 35 * 24 * 60 * 60
)

var legacyOpenAIOAuth429CounterIncrScript = redis.NewScript(`
	local count = redis.call('INCR', KEYS[1])
	if count == 1 then
		redis.call('EXPIRE', KEYS[1], tonumber(ARGV[1]))
	end
	return count
`)

var openAIOAuth429ObserveScript = redis.NewScript(`
	local key = KEYS[1]
	local revision = tonumber(ARGV[1])
	local window_seconds = tonumber(ARGV[2])
	local minimum_samples = tonumber(ARGV[3])
	local minimum_429 = tonumber(ARGV[4])
	local ratio_basis_points = tonumber(ARGV[5])
	local is_429 = tonumber(ARGV[6])
	local reset_at = tonumber(ARGV[7])
	local grace_seconds = tonumber(ARGV[8])
	local now = tonumber(redis.call('TIME')[1])

	local state = redis.call('HMGET', key, 'revision', 'window_start', 'total', 'count_429', 'latest_reset_at', 'triggered')
	local stored_revision = tonumber(state[1])
	local window_start = tonumber(state[2])

	if stored_revision and stored_revision ~= revision then
		redis.call('DEL', key)
		state = {false, false, false, false, false, false}
		window_start = nil
	end

	if window_start and now >= window_start + window_seconds then
		redis.call('DEL', key)
		state = {false, false, false, false, false, false}
		window_start = nil
	end

	if not window_start then
		if is_429 ~= 1 then
			return {0, 0, 0, 0, 0, 0, 0}
		end
		window_start = now
		local normalized_reset = 0
		if reset_at > now then
			normalized_reset = reset_at
		end
		redis.call('HSET', key,
			'revision', revision,
			'window_start', window_start,
			'total', 1,
			'count_429', 1,
			'latest_reset_at', normalized_reset,
			'triggered', 0)
		redis.call('EXPIREAT', key, window_start + window_seconds + grace_seconds)
		state = {revision, window_start, 1, 1, normalized_reset, 0}
	else
		local total = tonumber(state[3]) or 0
		local count_429 = tonumber(state[4]) or 0
		local latest_reset_at = tonumber(state[5]) or 0
		local triggered = tonumber(state[6]) or 0

		total = total + 1
		if is_429 == 1 then
			count_429 = count_429 + 1
			if reset_at > now then
				latest_reset_at = reset_at
			end
		end
		if latest_reset_at <= now then
			latest_reset_at = 0
		end
		redis.call('HSET', key,
			'total', total,
			'count_429', count_429,
			'latest_reset_at', latest_reset_at)
		state = {revision, window_start, total, count_429, latest_reset_at, triggered}
	end

	local total = tonumber(state[3]) or 0
	local count_429 = tonumber(state[4]) or 0
	local latest_reset_at = tonumber(state[5]) or 0
	local triggered = tonumber(state[6]) or 0
	local claimed = 0
	if triggered == 0 and total >= minimum_samples and count_429 >= minimum_429 and
		count_429 * 10000 >= total * ratio_basis_points then
		triggered = 1
		claimed = 1
		redis.call('HSET', key, 'triggered', 1)
	end

	return {1, window_start, total, count_429, latest_reset_at, triggered, claimed}
`)

type openAIOAuth429CounterCache struct {
	rdb *redis.Client
}

func NewOpenAIOAuth429CounterCache(rdb *redis.Client) service.OpenAIOAuth429CounterCache {
	return &openAIOAuth429CounterCache{rdb: rdb}
}

func (c *openAIOAuth429CounterCache) IncrementOpenAIOAuth429Count(ctx context.Context, accountID int64) (int64, error) {
	key := fmt.Sprintf("%s%d", legacyOpenAIOAuth429CounterPrefix, accountID)
	count, err := legacyOpenAIOAuth429CounterIncrScript.Run(ctx, c.rdb, []string{key}, legacyOpenAIOAuth429CounterTTL).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment legacy openai oauth 429 count: %w", err)
	}
	return count, nil
}

func (c *openAIOAuth429CounterCache) ResetOpenAIOAuth429Count(ctx context.Context, accountID int64) error {
	key := fmt.Sprintf("%s%d", legacyOpenAIOAuth429CounterPrefix, accountID)
	return c.rdb.Del(ctx, key).Err()
}

func (c *openAIOAuth429CounterCache) ObserveOpenAIOAuth429(
	ctx context.Context,
	observation service.OpenAIOAuth429Observation,
) (service.OpenAIOAuth429ObservationResult, error) {
	if err := observation.Validate(); err != nil {
		return service.OpenAIOAuth429ObservationResult{}, err
	}
	if c == nil || c.rdb == nil {
		return service.OpenAIOAuth429ObservationResult{}, fmt.Errorf("openai oauth 429 Redis cache is unavailable")
	}

	is429 := 0
	if observation.Is429 {
		is429 = 1
	}
	key := fmt.Sprintf("%s%d", openAIOAuth429CounterPrefix, observation.AccountID)
	values, err := openAIOAuth429ObserveScript.Run(ctx, c.rdb, []string{key},
		observation.PolicyRevision,
		observation.WindowSeconds,
		observation.MinimumSamples,
		observation.Minimum429Count,
		observation.RatioThresholdBasisPoints,
		is429,
		observation.ResetAtUnix,
		openAIOAuth429CounterGraceSeconds,
	).Int64Slice()
	if err != nil {
		return service.OpenAIOAuth429ObservationResult{}, fmt.Errorf("observe openai oauth 429 window: %w", err)
	}
	if len(values) != 7 {
		return service.OpenAIOAuth429ObservationResult{}, fmt.Errorf("observe openai oauth 429 window: unexpected result length %d", len(values))
	}
	return service.OpenAIOAuth429ObservationResult{
		Active:            values[0] == 1,
		WindowStartUnix:   values[1],
		TotalSamples:      values[2],
		Count429:          values[3],
		LatestResetAtUnix: values[4],
		Triggered:         values[5] == 1,
		TriggerClaimed:    values[6] == 1,
	}, nil
}
