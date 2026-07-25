package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAIOAuth429CounterStub struct {
	counts     []int64
	incrErr    error
	resetCalls []int64
}

func (s *openAIOAuth429CounterStub) IncrementOpenAIOAuth429Count(context.Context, int64) (int64, error) {
	if s.incrErr != nil {
		return 0, s.incrErr
	}
	count := s.counts[0]
	s.counts = s.counts[1:]
	return count, nil
}

func (s *openAIOAuth429CounterStub) ResetOpenAIOAuth429Count(_ context.Context, accountID int64) error {
	s.resetCalls = append(s.resetCalls, accountID)
	return nil
}

func TestRateLimitService_ShouldSuppressOpenAIOAuth429CooldownUntilThreshold(t *testing.T) {
	counter := &openAIOAuth429CounterStub{counts: []int64{1, 4, 5}}
	svc := &RateLimitService{openAIOAuth429Counter: counter}
	account := &Account{ID: 42}

	require.True(t, svc.shouldSuppressOpenAIOAuth429Cooldown(context.Background(), account))
	require.True(t, svc.shouldSuppressOpenAIOAuth429Cooldown(context.Background(), account))
	require.False(t, svc.shouldSuppressOpenAIOAuth429Cooldown(context.Background(), account))
	require.Equal(t, []int64{account.ID}, counter.resetCalls)
}

func TestRateLimitService_ShouldSuppressOpenAIOAuth429CooldownFallsBackOnRedisError(t *testing.T) {
	svc := &RateLimitService{
		openAIOAuth429Counter: &openAIOAuth429CounterStub{incrErr: errors.New("redis unavailable")},
	}

	require.False(t, svc.shouldSuppressOpenAIOAuth429Cooldown(context.Background(), &Account{ID: 42}))
}

func TestOpenAIOAuth429ThresholdPolicyContext(t *testing.T) {
	ctx := WithOpenAIOAuth429ThresholdPolicy(context.Background())
	require.True(t, isOpenAIOAuth429ThresholdPolicyEligible(ctx))
	require.False(t, isOpenAIOAuth429CooldownSuppressed(ctx))
	require.True(t, isOpenAIOAuth429CooldownSuppressed(withOpenAIOAuth429CooldownSuppressed(ctx, true)))
}
