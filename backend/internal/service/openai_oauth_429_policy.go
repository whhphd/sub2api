package service

import (
	"context"
	"log/slog"
)

type openAIOAuth429PolicyEligibleContextKey struct{}
type openAIOAuth429CooldownSuppressedContextKey struct{}

// WithOpenAIOAuth429ThresholdPolicy marks text gateway requests that are
// eligible for the account-level consecutive 429 policy.
func WithOpenAIOAuth429ThresholdPolicy(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIOAuth429PolicyEligibleContextKey{}, true)
}

func isOpenAIOAuth429ThresholdPolicyEligible(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(openAIOAuth429PolicyEligibleContextKey{}).(bool)
	return enabled
}

func withOpenAIOAuth429CooldownSuppressed(ctx context.Context, suppressed bool) context.Context {
	return context.WithValue(ctx, openAIOAuth429CooldownSuppressedContextKey{}, suppressed)
}

func isOpenAIOAuth429CooldownSuppressed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	suppressed, _ := ctx.Value(openAIOAuth429CooldownSuppressedContextKey{}).(bool)
	return suppressed
}

// shouldSuppressOpenAIOAuth429Cooldown increments the account streak once for
// the current upstream response. Cache failures deliberately fall back to the
// original cooldown behavior.
func (s *RateLimitService) shouldSuppressOpenAIOAuth429Cooldown(ctx context.Context, account *Account) bool {
	if s == nil || s.openAIOAuth429Counter == nil || account == nil || account.ID <= 0 {
		return false
	}
	threshold := account.GetOpenAIOAuth429ConsecutiveThreshold()
	count, err := s.openAIOAuth429Counter.IncrementOpenAIOAuth429Count(ctx, account.ID)
	if err != nil {
		slog.Warn("openai_oauth_429_counter_increment_failed", "account_id", account.ID, "error", err)
		return false
	}
	if count >= threshold {
		s.resetOpenAIOAuth429Counter(ctx, account.ID)
		return false
	}
	slog.Info("openai_oauth_429_cooldown_deferred",
		"account_id", account.ID,
		"consecutive_429", count,
		"threshold", threshold,
	)
	return true
}

func (s *RateLimitService) resetOpenAIOAuth429Counter(ctx context.Context, accountID int64) {
	if s == nil || s.openAIOAuth429Counter == nil || accountID <= 0 {
		return
	}
	if err := s.openAIOAuth429Counter.ResetOpenAIOAuth429Count(ctx, accountID); err != nil {
		slog.Warn("openai_oauth_429_counter_reset_failed", "account_id", accountID, "error", err)
	}
}
