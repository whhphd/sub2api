//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestProxyHealthCacheTripsAtThresholdAndSuccessResetsCircuit(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &proxyHealthCache{rdb: client}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	state, err := cache.RecordProxyFailure(ctx, 42, now, time.Minute, 2, 10*time.Minute, "transient", "timeout")
	require.NoError(t, err)
	require.Equal(t, 1, state.ConsecutiveFailures)
	require.Nil(t, state.OpenUntil)

	state, err = cache.RecordProxyFailure(ctx, 42, now.Add(time.Second), time.Minute, 2, 10*time.Minute, "transient", "timeout")
	require.NoError(t, err)
	require.Equal(t, 2, state.ConsecutiveFailures)
	require.NotNil(t, state.OpenUntil)
	require.True(t, state.OpenUntil.After(now))

	stored, err := cache.GetProxyHealth(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, 2, stored.ConsecutiveFailures)
	require.NotNil(t, stored.OpenUntil)

	require.NoError(t, cache.RecordProxySuccess(ctx, 42, now.Add(2*time.Second)))
	stored, err = cache.GetProxyHealth(ctx, 42)
	require.NoError(t, err)
	require.Zero(t, stored.ConsecutiveFailures)
	require.Nil(t, stored.OpenUntil)
	require.False(t, stored.LastSuccessAt.IsZero())
	ttl := server.TTL(proxyHealthKey(42))
	require.Greater(t, ttl, time.Hour)
}
