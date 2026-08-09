package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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
	require.False(t, disabled.NoopToolcallInjectionEnabled)
	require.False(t, disabled.Dynamic429Scheduling.Enabled)
	require.Equal(t, 300, disabled.Dynamic429Scheduling.WindowSeconds)
	require.Equal(t, 20, disabled.Dynamic429Scheduling.MinimumSamples)
	require.Equal(t, 3, disabled.Dynamic429Scheduling.Minimum429Count)
	require.Equal(t, 1.0, disabled.Dynamic429Scheduling.RatioThreshold)
	require.Equal(t, OpenAIOAuth429PauseModeUpstreamReset, disabled.Dynamic429Scheduling.PauseMode)
	require.Equal(t, 60, disabled.Dynamic429Scheduling.FixedPauseSeconds)
	require.Equal(t, int64(1), disabled.Dynamic429Scheduling.Revision)

	enabled := DefaultOpenAIOAuthRuntimeSettings(true)
	require.True(t, enabled.NoopToolcallInjectionEnabled)
	require.True(t, enabled.Dynamic429Scheduling.Enabled)
}

func TestNormalizeOpenAIOAuthRuntimeSettingsValidBoundaries(t *testing.T) {
	for _, settings := range []*OpenAIOAuthRuntimeSettings{
		{
			Dynamic429Scheduling: OpenAIOAuthDynamic429SchedulingSettings{
				Enabled: true, WindowSeconds: 60, MinimumSamples: 2, Minimum429Count: 1,
				RatioThreshold: 0.01, PauseMode: OpenAIOAuth429PauseModeUpstreamReset,
				FixedPauseSeconds: 1,
			},
		},
		{
			Dynamic429Scheduling: OpenAIOAuthDynamic429SchedulingSettings{
				Enabled: true, WindowSeconds: 3600, MinimumSamples: 10000, Minimum429Count: 10000,
				RatioThreshold: 1, PauseMode: OpenAIOAuth429PauseModeFixed,
				FixedPauseSeconds: 7200, Revision: 9,
			},
		},
	} {
		normalized, err := normalizeOpenAIOAuthRuntimeSettings(settings)
		require.NoError(t, err)
		require.GreaterOrEqual(t, normalized.Dynamic429Scheduling.Revision, int64(1))
	}
}

func TestNormalizeOpenAIOAuthRuntimeSettingsRejectsEnabledInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OpenAIOAuthDynamic429SchedulingSettings)
		match  string
	}{
		{name: "window low", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.WindowSeconds = 59 }, match: "window_seconds"},
		{name: "window high", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.WindowSeconds = 3601 }, match: "window_seconds"},
		{name: "samples low", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.MinimumSamples = 1 }, match: "minimum_samples"},
		{name: "samples high", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.MinimumSamples = 10001 }, match: "minimum_samples"},
		{name: "429 low", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.Minimum429Count = 0 }, match: "minimum_429_count"},
		{name: "429 above samples", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.Minimum429Count = v.MinimumSamples + 1 }, match: "minimum_429_count"},
		{name: "ratio low", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.RatioThreshold = 0.009 }, match: "ratio_threshold"},
		{name: "ratio high", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.RatioThreshold = 1.01 }, match: "ratio_threshold"},
		{name: "ratio nan", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.RatioThreshold = math.NaN() }, match: "ratio_threshold"},
		{name: "pause mode", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.PauseMode = "other" }, match: "pause_mode"},
		{name: "pause low", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.FixedPauseSeconds = 0 }, match: "fixed_pause_seconds"},
		{name: "pause high", mutate: func(v *OpenAIOAuthDynamic429SchedulingSettings) { v.FixedPauseSeconds = 7201 }, match: "fixed_pause_seconds"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := DefaultOpenAIOAuthRuntimeSettings(true)
			tt.mutate(&settings.Dynamic429Scheduling)
			_, err := normalizeOpenAIOAuthRuntimeSettings(settings)
			require.ErrorContains(t, err, tt.match)
		})
	}
}

func TestNormalizeOpenAIOAuthRuntimeSettingsRepairsDisabledValues(t *testing.T) {
	settings := &OpenAIOAuthRuntimeSettings{
		NoopToolcallInjectionEnabled: true,
		Dynamic429Scheduling: OpenAIOAuthDynamic429SchedulingSettings{
			WindowSeconds: -1, MinimumSamples: -1, Minimum429Count: 999,
			RatioThreshold: math.Inf(1), PauseMode: "bad", FixedPauseSeconds: -1,
		},
	}

	normalized, err := normalizeOpenAIOAuthRuntimeSettings(settings)
	require.NoError(t, err)
	require.True(t, normalized.NoopToolcallInjectionEnabled)
	require.False(t, normalized.Dynamic429Scheduling.Enabled)
	require.Equal(t, DefaultOpenAIOAuthRuntimeSettings(false).Dynamic429Scheduling, normalized.Dynamic429Scheduling)
}

func TestOpenAIOAuth429RatioBasisPoints(t *testing.T) {
	require.Equal(t, int64(100), openAIOAuth429RatioBasisPoints(0.01))
	require.Equal(t, int64(3333), openAIOAuth429RatioBasisPoints(0.3333))
	require.Equal(t, int64(10000), openAIOAuth429RatioBasisPoints(1))
}

func TestGetOpenAIOAuthRuntimeSettingsUsesLegacyOnlyWhenNewSettingMissing(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	repo.values[SettingKeyOpenAIOAuthNewAccountNoopToolcallDefaultsEnabled] = "true"
	svc := NewSettingService(repo, nil)

	legacy := svc.GetOpenAIOAuthRuntimeSettings(context.Background())
	require.True(t, legacy.NoopToolcallInjectionEnabled)
	require.True(t, legacy.Dynamic429Scheduling.Enabled)

	stored := DefaultOpenAIOAuthRuntimeSettings(false)
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	repo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(data)
	svc.openAIOAuthRuntimeSettingsCache.Store(&cachedOpenAIOAuthRuntimeSettings{settings: legacy, expiresAt: 0})

	current := svc.GetOpenAIOAuthRuntimeSettings(context.Background())
	require.False(t, current.NoopToolcallInjectionEnabled)
	require.False(t, current.Dynamic429Scheduling.Enabled)
}

func TestUpdateOpenAIOAuthRuntimeSettingsIsPartialAndAdvancesDynamicRevision(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	svc := NewSettingService(repo, nil)
	callbackCount := 0
	svc.SetOnUpdateCallback(func() { callbackCount++ })

	enabled := true
	afterInjection, err := svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), &enabled, nil)
	require.NoError(t, err)
	require.True(t, afterInjection.NoopToolcallInjectionEnabled)
	require.False(t, afterInjection.Dynamic429Scheduling.Enabled)
	require.Equal(t, int64(1), afterInjection.Dynamic429Scheduling.Revision)

	dynamic := afterInjection.Dynamic429Scheduling
	dynamic.Enabled = true
	dynamic.WindowSeconds = 600
	dynamic.Revision = 999
	afterDynamic, err := svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, &dynamic)
	require.NoError(t, err)
	require.True(t, afterDynamic.NoopToolcallInjectionEnabled)
	require.True(t, afterDynamic.Dynamic429Scheduling.Enabled)
	require.Equal(t, 600, afterDynamic.Dynamic429Scheduling.WindowSeconds)
	require.Equal(t, int64(2), afterDynamic.Dynamic429Scheduling.Revision)
	require.Equal(t, 2, callbackCount)
}

func TestGetOpenAIOAuthRuntimeSettingsRetainsLastKnownGoodOnReadFailure(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	stored := DefaultOpenAIOAuthRuntimeSettings(true)
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	repo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(data)
	svc := NewSettingService(repo, nil)

	first := svc.GetOpenAIOAuthRuntimeSettings(context.Background())
	require.True(t, first.Dynamic429Scheduling.Enabled)
	repo.readErr = errors.New("database unavailable")
	svc.openAIOAuthRuntimeSettingsCache.Store(&cachedOpenAIOAuthRuntimeSettings{
		settings:  first,
		expiresAt: time.Now().Add(-time.Second).UnixNano(),
	})

	fallback := svc.GetOpenAIOAuthRuntimeSettings(context.Background())
	require.True(t, fallback.NoopToolcallInjectionEnabled)
	require.True(t, fallback.Dynamic429Scheduling.Enabled)
}

func TestGetOpenAIOAuthRuntimeSettingsCorruptJSONFailsClosedWithoutCache(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	repo.values[SettingKeyOpenAIOAuthRuntimeSettings] = "{"
	svc := NewSettingService(repo, nil)

	settings := svc.GetOpenAIOAuthRuntimeSettings(context.Background())
	require.False(t, settings.NoopToolcallInjectionEnabled)
	require.False(t, settings.Dynamic429Scheduling.Enabled)
}
