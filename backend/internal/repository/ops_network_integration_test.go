//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestOpsNetworkPersistenceRollupAndCleanup(t *testing.T) {
	ctx := context.Background()
	repo := &opsRepository{db: integrationDB}
	start := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	peak := 800.0
	bucket := service.OpsNetworkBucket{ServerID: "network-integration", Link: "public", Device: "enp6s0", BucketStart: start, RXBytes: 6e9, TXBytes: 3e9, ValidSeconds: 60, RXPeakMbps: &peak, TXPeakMbps: &peak, RXCapacityMbps: 1000, TXCapacityMbps: 1000}
	for range 2 {
		require.NoError(t, repo.UpsertNetworkMinutes(ctx, []service.OpsNetworkBucket{bucket}))
	}
	rows, err := repo.GetNetworkBuckets(ctx, bucket.ServerID, "public", start, start.Add(time.Hour), 60)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 6e9, rows[0].RXBytes)
	bucket.BucketStart = start.Add(time.Minute)
	require.NoError(t, repo.UpsertNetworkMinutes(ctx, []service.OpsNetworkBucket{bucket}))
	for range 2 {
		require.NoError(t, repo.AggregateNetworkHours(ctx, bucket.ServerID, start, start.Add(time.Hour)))
	}
	rows, err = repo.GetNetworkBuckets(ctx, bucket.ServerID, "public", start, start.Add(time.Hour), 3600)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 12e9, rows[0].RXBytes)
	require.Equal(t, 120.0, rows[0].ValidSeconds)
	require.Equal(t, 800.0, *rows[0].RXPeakMbps)
	require.NoError(t, repo.CleanupNetworkMetrics(ctx, start.Add(time.Hour), start))
	rows, err = repo.GetNetworkBuckets(ctx, bucket.ServerID, "public", start, start.Add(time.Hour), 60)
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = repo.GetNetworkBuckets(ctx, bucket.ServerID, "public", start, start.Add(time.Hour), 3600)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
