package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuth429CounterCache_IncrementTTLAndReset(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = client.Close() }()
	cache := NewOpenAIOAuth429CounterCache(client)
	ctx := context.Background()
	const accountID int64 = 42

	count, err := cache.IncrementOpenAIOAuth429Count(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	count, err = cache.IncrementOpenAIOAuth429Count(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	key := fmt.Sprintf("%s%d", openAIOAuth429CounterPrefix, accountID)
	require.Equal(t, 35*24*time.Hour, server.TTL(key))
	require.NoError(t, cache.ResetOpenAIOAuth429Count(ctx, accountID))
	require.False(t, server.Exists(key))
}
