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

func TestRateLimitService_ShouldSuppressOpenAIOAuth429CooldownUntilConfiguredThreshold(t *testing.T) {
	tests := []struct {
		name       string
		account    *Account
		counts     []int64
		suppressed []bool
	}{
		{
			name:       "missing setting defaults to ten",
			account:    &Account{ID: 42},
			counts:     []int64{1, 9, 10},
			suppressed: []bool{true, true, false},
		},
		{
			name: "custom threshold",
			account: &Account{ID: 43, Extra: map[string]any{
				openAIOAuth429ConsecutiveThresholdExtraKey: 3,
			}},
			counts:     []int64{1, 2, 3},
			suppressed: []bool{true, true, false},
		},
		{
			name: "minimum threshold pauses immediately",
			account: &Account{ID: 44, Extra: map[string]any{
				openAIOAuth429ConsecutiveThresholdExtraKey: 1,
			}},
			counts:     []int64{1},
			suppressed: []bool{false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counter := &openAIOAuth429CounterStub{counts: tt.counts}
			svc := &RateLimitService{openAIOAuth429Counter: counter}
			for i, want := range tt.suppressed {
				require.Equal(t, want, svc.shouldSuppressOpenAIOAuth429Cooldown(context.Background(), tt.account), "call %d", i+1)
			}
			require.Equal(t, []int64{tt.account.ID}, counter.resetCalls)
		})
	}
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
