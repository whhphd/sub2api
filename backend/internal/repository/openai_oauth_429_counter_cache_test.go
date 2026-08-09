package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

var openAIOAuth429CounterTestNow = time.Unix(1_800_000_000, 0).UTC()

func newOpenAIOAuth429Observation(accountID int64) service.OpenAIOAuth429Observation {
	return service.OpenAIOAuth429Observation{
		AccountID:                 accountID,
		PolicyRevision:            1,
		WindowSeconds:             300,
		MinimumSamples:            20,
		Minimum429Count:           3,
		RatioThresholdBasisPoints: 10000,
	}
}

func newOpenAIOAuth429CounterCacheTest(t *testing.T) (*miniredis.Miniredis, *redis.Client, service.OpenAIOAuth429ObservationCache) {
	t.Helper()
	server := miniredis.RunT(t)
	server.SetTime(openAIOAuth429CounterTestNow)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewOpenAIOAuth429CounterCache(client).(service.OpenAIOAuth429ObservationCache)
	require.True(t, ok)
	return server, client, cache
}

func TestOpenAIOAuth429CounterCacheStartsOnlyOnFirst429(t *testing.T) {
	server, _, cache := newOpenAIOAuth429CounterCacheTest(t)
	ctx := context.Background()
	observation := newOpenAIOAuth429Observation(42)

	result, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.False(t, result.Active)
	require.False(t, server.Exists(fmt.Sprintf("%s%d", openAIOAuth429CounterPrefix, observation.AccountID)))

	observation.Is429 = true
	result, err = cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.True(t, result.Active)
	require.Equal(t, int64(1), result.TotalSamples)
	require.Equal(t, int64(1), result.Count429)
	require.False(t, result.Triggered)
}

func TestOpenAIOAuth429CounterCacheAnchorsWindowAndResetsAfterExpiry(t *testing.T) {
	server, _, cache := newOpenAIOAuth429CounterCacheTest(t)
	ctx := context.Background()
	observation := newOpenAIOAuth429Observation(43)
	observation.Is429 = true

	first, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	server.SetTime(openAIOAuth429CounterTestNow.Add(299 * time.Second))
	observation.Is429 = false
	last, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.Equal(t, first.WindowStartUnix, last.WindowStartUnix)
	require.Equal(t, int64(2), last.TotalSamples)

	server.SetTime(openAIOAuth429CounterTestNow.Add(300 * time.Second))
	expired, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.False(t, expired.Active)

	observation.Is429 = true
	restarted, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.True(t, restarted.Active)
	require.Greater(t, restarted.WindowStartUnix, first.WindowStartUnix)
	require.Equal(t, int64(1), restarted.TotalSamples)
}

func TestOpenAIOAuth429CounterCachePolicyRevisionDiscardsOldWindow(t *testing.T) {
	_, _, cache := newOpenAIOAuth429CounterCacheTest(t)
	ctx := context.Background()
	observation := newOpenAIOAuth429Observation(44)
	observation.Is429 = true
	_, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)

	observation.PolicyRevision = 2
	observation.Is429 = false
	result, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.False(t, result.Active)

	observation.Is429 = true
	result, err = cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.TotalSamples)
	require.Equal(t, int64(1), result.Count429)
}

func TestOpenAIOAuth429CounterCacheClaimsThresholdOnce(t *testing.T) {
	_, _, cache := newOpenAIOAuth429CounterCacheTest(t)
	ctx := context.Background()
	observation := newOpenAIOAuth429Observation(45)
	observation.MinimumSamples = 4
	observation.Minimum429Count = 2
	observation.RatioThresholdBasisPoints = 5000
	for _, is429 := range []bool{true, false, true} {
		observation.Is429 = is429
		result, err := cache.ObserveOpenAIOAuth429(ctx, observation)
		require.NoError(t, err)
		require.False(t, result.Triggered)
	}

	observation.Is429 = false
	result, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.True(t, result.Triggered)
	require.True(t, result.TriggerClaimed)

	result, err = cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.True(t, result.Triggered)
	require.False(t, result.TriggerClaimed)
}

func TestOpenAIOAuth429CounterCacheKeepsLatestFutureResetHint(t *testing.T) {
	server, client, cache := newOpenAIOAuth429CounterCacheTest(t)
	ctx := context.Background()
	observation := newOpenAIOAuth429Observation(46)
	observation.Is429 = true
	redisTime, err := client.Time(ctx).Result()
	require.NoError(t, err)
	firstReset := redisTime.Add(10 * time.Minute).Unix()
	observation.ResetAtUnix = firstReset
	result, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.Equal(t, firstReset, result.LatestResetAtUnix)

	redisTime, err = client.Time(ctx).Result()
	require.NoError(t, err)
	secondReset := redisTime.Add(5 * time.Minute).Unix()
	observation.ResetAtUnix = secondReset
	result, err = cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.Equal(t, secondReset, result.LatestResetAtUnix)

	observation.Is429 = false
	server.SetTime(openAIOAuth429CounterTestNow.Add(5 * time.Minute))
	result, err = cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)
	require.Zero(t, result.LatestResetAtUnix)
}

func TestOpenAIOAuth429CounterCacheConcurrentThresholdHasSingleClaim(t *testing.T) {
	_, _, cache := newOpenAIOAuth429CounterCacheTest(t)
	ctx := context.Background()
	observation := newOpenAIOAuth429Observation(47)
	observation.MinimumSamples = 2
	observation.Minimum429Count = 2
	observation.RatioThresholdBasisPoints = 10000
	observation.Is429 = true
	_, err := cache.ObserveOpenAIOAuth429(ctx, observation)
	require.NoError(t, err)

	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := 0
	errorsSeen := make([]error, 0)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, observeErr := cache.ObserveOpenAIOAuth429(ctx, observation)
			mu.Lock()
			defer mu.Unlock()
			if observeErr != nil {
				errorsSeen = append(errorsSeen, observeErr)
			}
			if result.TriggerClaimed {
				claims++
			}
		}()
	}
	wg.Wait()
	require.Empty(t, errorsSeen)
	require.Equal(t, 1, claims)
}

func TestOpenAIOAuth429CounterCacheRejectsInvalidObservation(t *testing.T) {
	_, _, cache := newOpenAIOAuth429CounterCacheTest(t)
	observation := newOpenAIOAuth429Observation(0)

	_, err := cache.ObserveOpenAIOAuth429(context.Background(), observation)
	require.ErrorContains(t, err, "account ID")
}
