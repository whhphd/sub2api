package service

import (
	"context"
	"log/slog"
	"time"
)

type openAIOAuth429PolicyEligibleContextKey struct{}
type openAIOAuth429CooldownSuppressedContextKey struct{}

// WithOpenAIOAuth429ThresholdPolicy marks gateway requests whose upstream
// outcomes are eligible for the global dynamic OpenAI OAuth 429 policy.
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

type openAIOAuthDynamic429Decision struct {
	Observed         bool
	SuppressCooldown bool
	Triggered        bool
	TriggerClaimed   bool
	PausedUntil      time.Time
}

// observeOpenAIOAuthDynamic429 submits one eligible outcome. A Redis failure
// returns Observed=false, which deliberately restores the original immediate
// 429 handling for the current response.
func (s *RateLimitService) observeOpenAIOAuthDynamic429(
	ctx context.Context,
	account *Account,
	is429 bool,
	resetAt time.Time,
) openAIOAuthDynamic429Decision {
	if s == nil || s.settingService == nil || !isOpenAIOAuthAccount(account) || account.IsShadow() || account.ID <= 0 {
		return openAIOAuthDynamic429Decision{}
	}
	settings := s.settingService.GetOpenAIOAuthRuntimeSettings(ctx)
	dynamic := settings.Dynamic429Scheduling
	if !dynamic.Enabled {
		return openAIOAuthDynamic429Decision{}
	}
	observer, ok := s.openAIOAuth429Counter.(OpenAIOAuth429ObservationCache)
	if !ok || observer == nil {
		slog.Warn("openai_oauth_dynamic_429_observer_unavailable", "account_id", account.ID)
		return openAIOAuthDynamic429Decision{}
	}

	resetAtUnix := int64(0)
	if resetAt.After(time.Now()) {
		resetAtUnix = resetAt.Unix()
	}
	result, err := observer.ObserveOpenAIOAuth429(ctx, OpenAIOAuth429Observation{
		AccountID:                 account.ID,
		PolicyRevision:            dynamic.Revision,
		WindowSeconds:             dynamic.WindowSeconds,
		MinimumSamples:            dynamic.MinimumSamples,
		Minimum429Count:           dynamic.Minimum429Count,
		RatioThresholdBasisPoints: openAIOAuth429RatioBasisPoints(dynamic.RatioThreshold),
		Is429:                     is429,
		ResetAtUnix:               resetAtUnix,
	})
	if err != nil {
		slog.Warn("openai_oauth_dynamic_429_observation_failed", "account_id", account.ID, "is_429", is429, "error", err)
		return openAIOAuthDynamic429Decision{}
	}

	decision := openAIOAuthDynamic429Decision{
		Observed:         true,
		SuppressCooldown: is429,
		Triggered:        result.Triggered,
		TriggerClaimed:   result.TriggerClaimed,
	}
	if !result.TriggerClaimed {
		if is429 && !result.Triggered {
			slog.Info("openai_oauth_dynamic_429_cooldown_deferred",
				"account_id", account.ID,
				"total_samples", result.TotalSamples,
				"count_429", result.Count429,
				"policy_revision", dynamic.Revision,
			)
		}
		return decision
	}

	pauseUntil, shouldPause := s.resolveOpenAIOAuthDynamic429Pause(ctx, account, dynamic, result.LatestResetAtUnix)
	if !shouldPause {
		slog.Warn("openai_oauth_dynamic_429_trigger_without_pause",
			"account_id", account.ID,
			"pause_mode", dynamic.PauseMode,
			"reason", "no future upstream reset and fallback cooldown disabled",
		)
		return decision
	}
	decision.PausedUntil = pauseUntil
	s.applyOpenAIOAuthDynamic429Pause(ctx, account, pauseUntil)
	return decision
}

func (s *RateLimitService) resolveOpenAIOAuthDynamic429Pause(
	ctx context.Context,
	account *Account,
	dynamic OpenAIOAuthDynamic429SchedulingSettings,
	latestResetAtUnix int64,
) (time.Time, bool) {
	now := time.Now()
	if dynamic.PauseMode == OpenAIOAuth429PauseModeFixed {
		return now.Add(time.Duration(dynamic.FixedPauseSeconds) * time.Second), true
	}
	if latestResetAtUnix > now.Unix() {
		return time.Unix(latestResetAtUnix, 0), true
	}
	cooldown, enabled := s.get429FallbackCooldown(ctx, account)
	if !enabled || cooldown <= 0 {
		return time.Time{}, false
	}
	return now.Add(cooldown), true
}

func (s *RateLimitService) applyOpenAIOAuthDynamic429Pause(ctx context.Context, account *Account, pauseUntil time.Time) {
	s.notifyAccountSchedulingBlocked(account, pauseUntil, "openai_oauth_dynamic_429")
	if s.accountRepo != nil {
		if err := s.accountRepo.SetRateLimited(ctx, account.ID, pauseUntil); err != nil {
			slog.Warn("openai_oauth_dynamic_429_set_rate_limited_failed", "account_id", account.ID, "reset_at", pauseUntil, "error", err)
			return
		}
	}
	slog.Warn("openai_oauth_dynamic_429_account_paused",
		"account_id", account.ID,
		"reset_at", pauseUntil,
		"pause_for", time.Until(pauseUntil).Truncate(time.Second),
	)
}

// resetOpenAIOAuth429Counter is retained until all staged call sites have moved
// from streak resets to fixed-window outcome observations. Resetting here would
// violate the anchored-window contract, so it is intentionally a no-op.
func (s *RateLimitService) resetOpenAIOAuth429Counter(context.Context, int64) {}
