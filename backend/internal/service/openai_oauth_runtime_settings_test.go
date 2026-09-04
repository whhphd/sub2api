package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIOAuthRuntimeSettingRepo struct {
	mu       sync.Mutex
	values   map[string]string
	readErr  error
	writeErr error
	reads    int
}

func newOpenAIOAuthRuntimeSettingRepo() *openAIOAuthRuntimeSettingRepo {
	return &openAIOAuthRuntimeSettingRepo{values: make(map[string]string)}
}

func (r *openAIOAuthRuntimeSettingRepo) Get(ctx context.Context, key string) (*Setting, error) {
	value, err := r.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *openAIOAuthRuntimeSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.readErr != nil {
		return "", r.readErr
	}
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *openAIOAuthRuntimeSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return r.writeErr
	}
	r.values[key] = value
	return nil
}

func (r *openAIOAuthRuntimeSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *openAIOAuthRuntimeSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	for key, value := range settings {
		if err := r.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *openAIOAuthRuntimeSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *openAIOAuthRuntimeSettingRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

func TestDefaultOpenAIOAuthRuntimeSettings(t *testing.T) {
	disabled := DefaultOpenAIOAuthRuntimeSettings(false)
	require.False(t, disabled.SafePreOutputOverloadRetryEnabled)
	require.True(t, disabled.PlanGatedModelCooldownEnabled)
	require.False(t, disabled.OpenAIRateLimitSameAccountRetryEnabled)
	require.False(t, disabled.OpenAIRateLimitProxyRotationEnabled)
	require.False(t, disabled.OpenAIAutoResetCreditGlobalEnabled)
	require.False(t, disabled.GrokOAuthForbiddenSameAccountRetryEnabled)

	require.Equal(t, disabled, DefaultOpenAIOAuthRuntimeSettings(true))
}

func TestNormalizeOpenAIOAuthRuntimeSettingsClonesInput(t *testing.T) {
	input := &OpenAIOAuthRuntimeSettings{
		SafePreOutputOverloadRetryEnabled:         true,
		PlanGatedModelCooldownEnabled:             false,
		OpenAIRateLimitSameAccountRetryEnabled:    true,
		GrokOAuthForbiddenSameAccountRetryEnabled: true,
	}

	normalized, err := normalizeOpenAIOAuthRuntimeSettings(input)
	require.NoError(t, err)
	require.Equal(t, input, normalized)
	require.NotSame(t, input, normalized)

	defaults, err := normalizeOpenAIOAuthRuntimeSettings(nil)
	require.NoError(t, err)
	require.Equal(t, DefaultOpenAIOAuthRuntimeSettings(false), defaults)
}

func TestGetOpenAIOAuthRuntimeSettingsDefaultsPlanGatedCooldownForLegacyJSON(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	repo.values[SettingKeyOpenAIOAuthRuntimeSettings] = `{"safe_pre_output_overload_retry_enabled":false}`
	svc := NewSettingService(repo, nil)

	settings := svc.GetOpenAIOAuthRuntimeSettings(context.Background())
	require.True(t, settings.PlanGatedModelCooldownEnabled)
}

func TestUpdateOpenAIOAuthRuntimeSettingsIsPartial(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	svc := NewSettingService(repo, nil)
	callbackCount := 0
	svc.SetOnUpdateCallback(func() { callbackCount++ })

	safeRetryEnabled := true
	afterSafeRetry, err := svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), &safeRetryEnabled, nil)
	require.NoError(t, err)
	require.True(t, afterSafeRetry.SafePreOutputOverloadRetryEnabled)
	require.True(t, afterSafeRetry.PlanGatedModelCooldownEnabled)

	disabledPlanCooldown := false
	afterPlanCooldown, err := svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, &disabledPlanCooldown)
	require.NoError(t, err)
	require.False(t, afterPlanCooldown.PlanGatedModelCooldownEnabled)
	require.True(t, afterPlanCooldown.SafePreOutputOverloadRetryEnabled)

	rateLimitRetryEnabled := true
	afterRateLimitRetry, err := svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, &rateLimitRetryEnabled)
	require.NoError(t, err)
	require.True(t, afterRateLimitRetry.OpenAIRateLimitSameAccountRetryEnabled)
	require.False(t, afterRateLimitRetry.PlanGatedModelCooldownEnabled)

	grokForbiddenRetryEnabled := true
	afterGrokForbiddenRetry, err := svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, nil, &grokForbiddenRetryEnabled)
	require.NoError(t, err)
	require.True(t, afterGrokForbiddenRetry.GrokOAuthForbiddenSameAccountRetryEnabled)
	require.True(t, afterGrokForbiddenRetry.OpenAIRateLimitSameAccountRetryEnabled)
	require.Equal(t, 4, callbackCount)

	proxyRotationEnabled := true
	afterProxyRotation, err := svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, nil, nil, &proxyRotationEnabled)
	require.NoError(t, err)
	require.True(t, afterProxyRotation.OpenAIRateLimitProxyRotationEnabled)
	require.True(t, afterProxyRotation.GrokOAuthForbiddenSameAccountRetryEnabled)
	require.Equal(t, 5, callbackCount)

	autoResetGlobalEnabled := true
	afterAutoResetGlobal, err := svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, nil, nil, nil, &autoResetGlobalEnabled)
	require.NoError(t, err)
	require.True(t, afterAutoResetGlobal.OpenAIAutoResetCreditGlobalEnabled)
	require.True(t, afterAutoResetGlobal.OpenAIRateLimitProxyRotationEnabled)
	require.Equal(t, 6, callbackCount)

	var stored OpenAIOAuthRuntimeSettings
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyOpenAIOAuthRuntimeSettings]), &stored))
	require.Equal(t, afterAutoResetGlobal, &stored)

	_, err = svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil)
	require.ErrorContains(t, err, "at least one")
}

func TestGetOpenAIOAuthRuntimeSettingsRetainsLastKnownGoodOnReadFailure(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	stored := &OpenAIOAuthRuntimeSettings{
		SafePreOutputOverloadRetryEnabled:         true,
		PlanGatedModelCooldownEnabled:             false,
		OpenAIRateLimitSameAccountRetryEnabled:    true,
		GrokOAuthForbiddenSameAccountRetryEnabled: true,
	}
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	repo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(data)
	svc := NewSettingService(repo, nil)

	first := svc.GetOpenAIOAuthRuntimeSettings(context.Background())
	require.Equal(t, stored, first)
	repo.readErr = errors.New("database unavailable")
	svc.openAIOAuthRuntimeSettingsCache.Store(&cachedOpenAIOAuthRuntimeSettings{
		settings:  first,
		expiresAt: time.Now().Add(-time.Second).UnixNano(),
	})

	fallback := svc.GetOpenAIOAuthRuntimeSettings(context.Background())
	require.Equal(t, stored, fallback)
}

func TestGetOpenAIOAuthRuntimeSettingsCorruptJSONUsesDefaultsWithoutCache(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	repo.values[SettingKeyOpenAIOAuthRuntimeSettings] = "{"
	svc := NewSettingService(repo, nil)

	settings := svc.GetOpenAIOAuthRuntimeSettings(context.Background())
	require.Equal(t, DefaultOpenAIOAuthRuntimeSettings(false), settings)
}
