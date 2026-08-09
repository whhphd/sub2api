package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAI403CounterResetStub struct {
	resetCalls    []int64
	reset429Calls []int64
	observed429   []OpenAIOAuth429Observation
	result429     OpenAIOAuth429ObservationResult
}

func (s *openAI403CounterResetStub) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	return 0, nil
}

func (s *openAI403CounterResetStub) ResetOpenAI403Count(_ context.Context, accountID int64) error {
	s.resetCalls = append(s.resetCalls, accountID)
	return nil
}

func (s *openAI403CounterResetStub) IncrementOpenAIOAuth429Count(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *openAI403CounterResetStub) ResetOpenAIOAuth429Count(_ context.Context, accountID int64) error {
	s.reset429Calls = append(s.reset429Calls, accountID)
	return nil
}

func (s *openAI403CounterResetStub) ObserveOpenAIOAuth429(_ context.Context, observation OpenAIOAuth429Observation) (OpenAIOAuth429ObservationResult, error) {
	s.observed429 = append(s.observed429, observation)
	return s.result429, nil
}

func TestOpenAIGatewayServiceRecordUsage_ResetsOpenAI403CounterForZeroUsage(t *testing.T) {
	counter := &openAI403CounterResetStub{}
	rateLimitSvc := NewRateLimitService(nil, nil, nil, nil, nil)
	rateLimitSvc.SetOpenAI403CounterCache(counter)

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)
	svc.rateLimitService = rateLimitSvc

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_zero_usage_reset_403",
			Model:     "gpt-5.1",
		},
		APIKey:  &APIKey{ID: 1001, Group: &Group{RateMultiplier: 1}},
		User:    &User{ID: 2001},
		Account: &Account{ID: 777, Platform: PlatformOpenAI},
	})

	require.NoError(t, err)
	require.Equal(t, []int64{777}, counter.resetCalls)
	require.Equal(t, 1, usageRepo.calls)
}

func TestOpenAIGatewayServiceRecordUsage_ObservesEligibleOAuthSuccessWithoutResettingWindow(t *testing.T) {
	counter := &openAI403CounterResetStub{result429: OpenAIOAuth429ObservationResult{
		Active: true, TotalSamples: 20, Count429: 3, Triggered: true, TriggerClaimed: true,
	}}
	rateLimitSvc := NewRateLimitService(nil, nil, nil, nil, nil)
	rateLimitSvc.SetOpenAIOAuth429CounterCache(counter)
	settingRepo := newOpenAIOAuthRuntimeSettingRepo()
	settings := DefaultOpenAIOAuthRuntimeSettings(true)
	settings.Dynamic429Scheduling.PauseMode = OpenAIOAuth429PauseModeFixed
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	settingRepo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(settingsJSON)
	rateLimitSvc.SetSettingService(NewSettingService(settingRepo, nil))
	blocker := &openAIOAuth429RuntimeBlockerStub{}
	rateLimitSvc.SetAccountRuntimeBlocker(blocker)

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)
	svc.rateLimitService = rateLimitSvc
	account := &Account{ID: 778, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	err = svc.RecordUsage(WithOpenAIOAuth429ThresholdPolicy(context.Background()), &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_zero_usage_reset_429", Model: "gpt-5.1"},
		APIKey:  &APIKey{ID: 1002, Group: &Group{RateMultiplier: 1}},
		User:    &User{ID: 2002},
		Account: account,
	})

	require.NoError(t, err)
	require.Empty(t, counter.reset429Calls)
	require.Len(t, counter.observed429, 1)
	require.False(t, counter.observed429[0].Is429)
	require.Equal(t, account.ID, blocker.accountID, "a success-triggered pause applies only to later scheduling")
}

func TestOpenAIGatewayServiceRecordUsage_CyberFailureDoesNotDoubleCountAsSuccess(t *testing.T) {
	counter := &openAI403CounterResetStub{}
	rateLimitSvc := NewRateLimitService(nil, nil, nil, nil, nil)
	rateLimitSvc.SetOpenAIOAuth429CounterCache(counter)
	settingRepo := newOpenAIOAuthRuntimeSettingRepo()
	settingsJSON, err := json.Marshal(DefaultOpenAIOAuthRuntimeSettings(true))
	require.NoError(t, err)
	settingRepo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(settingsJSON)
	rateLimitSvc.SetSettingService(NewSettingService(settingRepo, nil))

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.rateLimitService = rateLimitSvc

	err = svc.RecordUsage(WithOpenAIOAuth429ThresholdPolicy(context.Background()), &OpenAIRecordUsageInput{
		Result:       &OpenAIForwardResult{RequestID: "resp_cyber", Model: "gpt-5.1"},
		APIKey:       &APIKey{ID: 1003, Group: &Group{RateMultiplier: 1}},
		User:         &User{ID: 2003},
		Account:      &Account{ID: 779, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		CyberBlocked: true,
	})

	require.NoError(t, err)
	require.Empty(t, counter.observed429)
}
