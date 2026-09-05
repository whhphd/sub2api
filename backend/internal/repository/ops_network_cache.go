package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

type opsNetworkCache struct{ rdb *redis.Client }

func NewOpsNetworkCache(rdb *redis.Client) service.OpsNetworkCache { return &opsNetworkCache{rdb: rdb} }

func networkKeys(server string) []string {
	prefix := "ops:network:{" + server + "}:"
	return []string{prefix + "lease", prefix + "snapshot", prefix + "samples", prefix + "last_at"}
}

var networkLeaseScript = redis.NewScript(`
local owner = redis.call('GET', KEYS[1])
if not owner or owner == ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', 15000)
  return 1
end
return 0`)

var networkCommitScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
local last = tonumber(redis.call('GET', KEYS[4]) or '0')
local now = tonumber(ARGV[2])
if now <= last then return 0 end
redis.call('SET', KEYS[2], ARGV[3], 'EX', ARGV[4])
redis.call('SET', KEYS[4], ARGV[2], 'EX', ARGV[4])
for i=5,#ARGV do redis.call('ZADD', KEYS[3], now, ARGV[i]) end
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now-tonumber(ARGV[4])*1000)
redis.call('EXPIRE', KEYS[3], ARGV[4])
return 1`)

func (c *opsNetworkCache) RenewNetworkLease(ctx context.Context, server, owner string) (bool, error) {
	n, err := networkLeaseScript.Run(ctx, c.rdb, networkKeys(server)[:1], owner).Int()
	return n == 1, err
}
func (c *opsNetworkCache) ReleaseNetworkLease(ctx context.Context, server, owner string) error {
	return c.rdb.Eval(ctx, `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`, networkKeys(server)[:1], owner).Err()
}
func (c *opsNetworkCache) CommitNetworkSnapshot(ctx context.Context, server, owner string, snapshot *service.OpsNetworkOverview, retention time.Duration) (bool, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return false, err
	}
	args := []any{owner, snapshot.CollectedAt.UnixMilli(), string(raw), int64(retention.Seconds())}
	for _, sample := range snapshot.Links {
		b, e := json.Marshal(sample)
		if e != nil {
			return false, e
		}
		args = append(args, string(b))
	}
	n, err := networkCommitScript.Run(ctx, c.rdb, networkKeys(server), args...).Int()
	return n == 1, err
}
func (c *opsNetworkCache) GetNetworkSnapshot(ctx context.Context, server string) (*service.OpsNetworkOverview, error) {
	raw, err := c.rdb.Get(ctx, networkKeys(server)[1]).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out service.OpsNetworkOverview
	err = json.Unmarshal(raw, &out)
	return &out, err
}
func (c *opsNetworkCache) GetNetworkSamples(ctx context.Context, server string, start, end time.Time) ([]service.OpsNetworkSample, error) {
	rows, err := c.rdb.ZRangeByScore(ctx, networkKeys(server)[2], &redis.ZRangeBy{Min: strconv.FormatInt(start.UnixMilli(), 10), Max: strconv.FormatInt(end.UnixMilli(), 10)}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]service.OpsNetworkSample, 0, len(rows))
	for _, row := range rows {
		var sample service.OpsNetworkSample
		if err := json.Unmarshal([]byte(row), &sample); err != nil {
			return nil, err
		}
		out = append(out, sample)
	}
	return out, nil
}
