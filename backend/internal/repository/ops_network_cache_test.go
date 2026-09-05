package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestOpsNetworkLeaseDeduplicationAndRetention(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewOpsNetworkCache(client)
	ctx := context.Background()
	at := time.Now()
	ok, err := cache.RenewNetworkLease(ctx, "host", "one")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.RenewNetworkLease(ctx, "host", "two")
	require.NoError(t, err)
	require.False(t, ok)
	snapshot := &service.OpsNetworkOverview{CollectedAt: at, Links: []service.OpsNetworkSample{{OpsNetworkLink: service.OpsNetworkLink{ID: "public"}, End: at, Status: "ok"}}}
	ok, err = cache.CommitNetworkSnapshot(ctx, "host", "two", snapshot, time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = cache.CommitNetworkSnapshot(ctx, "host", "one", snapshot, time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.CommitNetworkSnapshot(ctx, "host", "one", snapshot, time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
	samples, err := cache.GetNetworkSamples(ctx, "host", at.Add(-time.Second), at.Add(time.Second))
	require.NoError(t, err)
	require.Len(t, samples, 1)
	server.FastForward(16 * time.Second)
	ok, err = cache.RenewNetworkLease(ctx, "host", "two")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, cache.ReleaseNetworkLease(ctx, "host", "one"))
	snapshot.CollectedAt = at.Add(2 * time.Hour)
	snapshot.Links[0].End = snapshot.CollectedAt
	ok, err = cache.CommitNetworkSnapshot(ctx, "host", "two", snapshot, time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	samples, err = cache.GetNetworkSamples(ctx, "host", at.Add(-time.Second), at.Add(3*time.Hour))
	require.NoError(t, err)
	require.Len(t, samples, 1)
	server.FastForward(time.Hour + time.Second)
	stored, err := cache.GetNetworkSnapshot(ctx, "host")
	require.NoError(t, err)
	require.Nil(t, stored)
}
