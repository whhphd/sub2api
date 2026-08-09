package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIOAuth429CounterStub struct {
	results    []OpenAIOAuth429ObservationResult
	observeErr error
	observed   []OpenAIOAuth429Observation
}

func (s *openAIOAuth429CounterStub) IncrementOpenAIOAuth429Count(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *openAIOAuth429CounterStub) ResetOpenAIOAuth429Count(context.Context, int64) error {
	return nil
}

func (s *openAIOAuth429CounterStub) ObserveOpenAIOAuth429(_ context.Context, observation OpenAIOAuth429Observation) (OpenAIOAuth429ObservationResult, error) {
	s.observed = append(s.observed, observation)
	if s.observeErr != nil {
		return OpenAIOAuth429ObservationResult{}, s.observeErr
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

type openAIOAuth429PolicyAccountRepo struct {
	stubOpenAIAccountRepo
	rateLimitedID int64
	rateLimitedAt time.Time
}

func (r *openAIOAuth429PolicyAccountRepo) SetRateLimited(_ context.Context, accountID int64, resetAt time.Time) error {
	r.rateLimitedID = accountID
	r.rateLimitedAt = resetAt
	return nil
}

type openAIOAuth429RuntimeBlockerStub struct {
	accountID int64
	until     time.Time
	reason    string
}

func (s *openAIOAuth429RuntimeBlockerStub) BlockAccountScheduling(account *Account, until time.Time, reason string) {
	s.accountID = account.ID
	s.until = until
	s.reason = reason
}

func (s *openAIOAuth429RuntimeBlockerStub) ClearAccountSchedulingBlock(int64) {}

func newOpenAIOAuth429PolicyService(
	t *testing.T,
	dynamic OpenAIOAuthDynamic429SchedulingSettings,
	counter *openAIOAuth429CounterStub,
) (*RateLimitService, *openAIOAuth429PolicyAccountRepo, *openAIOAuth429RuntimeBlockerStub, *openAIOAuthRuntimeSettingRepo) {
	t.Helper()
	settingRepo := newOpenAIOAuthRuntimeSettingRepo()
	settings := DefaultOpenAIOAuthRuntimeSettings(false)
	settings.Dynamic429Scheduling = dynamic
	data, err := json.Marshal(settings)
	require.NoError(t, err)
	settingRepo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(data)
	accountRepo := &openAIOAuth429PolicyAccountRepo{}
	blocker := &openAIOAuth429RuntimeBlockerStub{}
	svc := NewRateLimitService(accountRepo, nil, nil, nil, nil)
	svc.SetSettingService(NewSettingService(settingRepo, nil))
	svc.SetOpenAIOAuth429CounterCache(counter)
	svc.SetAccountRuntimeBlocker(blocker)
	return svc, accountRepo, blocker, settingRepo
}

func TestObserveOpenAIOAuthDynamic429SuppressesCooldownBelowThreshold(t *testing.T) {
	dynamic := DefaultOpenAIOAuthRuntimeSettings(true).Dynamic429Scheduling
	counter := &openAIOAuth429CounterStub{results: []OpenAIOAuth429ObservationResult{{
		Active: true, TotalSamples: 1, Count429: 1,
	}}}
	svc, repo, blocker, _ := newOpenAIOAuth429PolicyService(t, dynamic, counter)
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	decision := svc.observeOpenAIOAuthDynamic429(context.Background(), account, true, time.Time{})
	require.True(t, decision.Observed)
	require.True(t, decision.SuppressCooldown)
	require.False(t, decision.Triggered)
	require.Zero(t, repo.rateLimitedID)
	require.Zero(t, blocker.accountID)
	require.Len(t, counter.observed, 1)
	require.True(t, counter.observed[0].Is429)
	require.Equal(t, int64(10000), counter.observed[0].RatioThresholdBasisPoints)
}

func TestObserveOpenAIOAuthDynamic429RedisFailureRestoresOriginalHandling(t *testing.T) {
	dynamic := DefaultOpenAIOAuthRuntimeSettings(true).Dynamic429Scheduling
	counter := &openAIOAuth429CounterStub{observeErr: errors.New("redis unavailable")}
	svc, _, _, _ := newOpenAIOAuth429PolicyService(t, dynamic, counter)
	account := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	decision := svc.observeOpenAIOAuthDynamic429(context.Background(), account, true, time.Time{})
	require.False(t, decision.Observed)
	require.False(t, decision.SuppressCooldown)
}

func TestObserveOpenAIOAuthDynamic429FixedPauseUsesOriginalRateLimitedState(t *testing.T) {
	dynamic := DefaultOpenAIOAuthRuntimeSettings(true).Dynamic429Scheduling
	dynamic.PauseMode = OpenAIOAuth429PauseModeFixed
	dynamic.FixedPauseSeconds = 90
	counter := &openAIOAuth429CounterStub{results: []OpenAIOAuth429ObservationResult{{
		Active: true, TotalSamples: 20, Count429: 3, Triggered: true, TriggerClaimed: true,
	}}}
	svc, repo, blocker, _ := newOpenAIOAuth429PolicyService(t, dynamic, counter)
	account := &Account{ID: 44, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	before := time.Now()

	decision := svc.observeOpenAIOAuthDynamic429(context.Background(), account, false, time.Time{})
	after := time.Now()
	require.True(t, decision.Observed)
	require.False(t, decision.SuppressCooldown, "a successful triggering outcome must remain successful")
	require.True(t, decision.TriggerClaimed)
	require.Equal(t, account.ID, repo.rateLimitedID)
	require.Equal(t, account.ID, blocker.accountID)
	require.Equal(t, "openai_oauth_dynamic_429", blocker.reason)
	require.False(t, repo.rateLimitedAt.Before(before.Add(90*time.Second)))
	require.False(t, repo.rateLimitedAt.After(after.Add(90*time.Second)))
}

func TestObserveOpenAIOAuthDynamic429UpstreamResetUsesWindowHint(t *testing.T) {
	dynamic := DefaultOpenAIOAuthRuntimeSettings(true).Dynamic429Scheduling
	resetAt := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	counter := &openAIOAuth429CounterStub{results: []OpenAIOAuth429ObservationResult{{
		Active: true, TotalSamples: 20, Count429: 3, Triggered: true, TriggerClaimed: true,
		LatestResetAtUnix: resetAt.Unix(),
	}}}
	svc, repo, _, _ := newOpenAIOAuth429PolicyService(t, dynamic, counter)
	account := &Account{ID: 45, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	decision := svc.observeOpenAIOAuthDynamic429(context.Background(), account, true, resetAt)
	require.True(t, decision.SuppressCooldown)
	require.Equal(t, resetAt, decision.PausedUntil)
	require.Equal(t, resetAt, repo.rateLimitedAt)
}

func TestObserveOpenAIOAuthDynamic429IgnoresDisabledAndNonOAuthAccounts(t *testing.T) {
	dynamic := DefaultOpenAIOAuthRuntimeSettings(false).Dynamic429Scheduling
	counter := &openAIOAuth429CounterStub{}
	svc, _, _, _ := newOpenAIOAuth429PolicyService(t, dynamic, counter)

	require.False(t, svc.observeOpenAIOAuthDynamic429(context.Background(), &Account{ID: 46, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, true, time.Time{}).Observed)
	dynamic.Enabled = true
	svc, _, _, _ = newOpenAIOAuth429PolicyService(t, dynamic, counter)
	require.False(t, svc.observeOpenAIOAuthDynamic429(context.Background(), &Account{ID: 47, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, true, time.Time{}).Observed)
	require.Empty(t, counter.observed)
}

func TestObserveOpenAIOAuthDynamic429UpstreamResetDoesNotManufactureDisabledFallback(t *testing.T) {
	dynamic := DefaultOpenAIOAuthRuntimeSettings(true).Dynamic429Scheduling
	counter := &openAIOAuth429CounterStub{results: []OpenAIOAuth429ObservationResult{{
		Active: true, TotalSamples: 20, Count429: 3, Triggered: true, TriggerClaimed: true,
	}}}
	svc, repo, blocker, settingRepo := newOpenAIOAuth429PolicyService(t, dynamic, counter)
	fallback, err := json.Marshal(RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 5})
	require.NoError(t, err)
	settingRepo.values[SettingKeyRateLimit429CooldownSettings] = string(fallback)
	account := &Account{ID: 48, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	decision := svc.observeOpenAIOAuthDynamic429(context.Background(), account, true, time.Time{})
	require.True(t, decision.TriggerClaimed)
	require.True(t, decision.SuppressCooldown)
	require.True(t, decision.PausedUntil.IsZero())
	require.Zero(t, repo.rateLimitedID)
	require.Zero(t, blocker.accountID)
}

func TestOpenAIOAuth429ThresholdPolicyContext(t *testing.T) {
	ctx := WithOpenAIOAuth429ThresholdPolicy(context.Background())
	require.True(t, isOpenAIOAuth429ThresholdPolicyEligible(ctx))
	require.False(t, isOpenAIOAuth429CooldownSuppressed(ctx))
	require.True(t, isOpenAIOAuth429CooldownSuppressed(withOpenAIOAuth429CooldownSuppressed(ctx, true)))
}
