package service

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func (r *openAIOAuthRuntimeSettingRepo) CompareAndSwap(_ context.Context, key, oldValue, newValue string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return false, r.writeErr
	}
	if r.values[key] != oldValue {
		return false, nil
	}
	r.values[key] = newValue
	return true, nil
}

func TestCodexEnhancementRuntimeSwitchesAreMutuallyExclusive(t *testing.T) {
	ctx := context.Background()
	repo := newOpenAIOAuthRuntimeSettingRepo()
	svc := &SettingService{settingRepo: repo}
	yes, no := true, false
	settings, err := svc.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, &yes, nil, nil)
	require.NoError(t, err)
	require.True(t, settings.OpenAIRateLimitProxyRotationEnabled)
	settings, err = svc.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, &yes)
	require.NoError(t, err)
	require.True(t, settings.CodexFingerprintEnhancementEnabled)
	require.False(t, settings.OpenAIRateLimitProxyRotationEnabled)
	require.True(t, svc.GetOpenAIOAuthRuntimeSettings(ctx).CodexFingerprintEnhancementEnabled)
	_, err = svc.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, &yes, nil, &yes)
	require.Error(t, err)
	require.True(t, svc.GetOpenAIOAuthRuntimeSettings(ctx).CodexFingerprintEnhancementEnabled)
	settings, err = svc.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, &no)
	require.NoError(t, err)
	require.False(t, settings.CodexFingerprintEnhancementEnabled)
	require.False(t, settings.OpenAIRateLimitProxyRotationEnabled)
	settings, err = svc.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, &yes)
	require.NoError(t, err)
	require.True(t, settings.CodexFingerprintEnhancementEnabled)
	settings, err = svc.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, &yes, nil, nil)
	require.NoError(t, err)
	require.False(t, settings.CodexFingerprintEnhancementEnabled)
	require.True(t, settings.OpenAIRateLimitProxyRotationEnabled)
}

func TestCodexEnhancementConcurrentPartialUpdatesKeepUnrelatedPolicies(t *testing.T) {
	ctx := context.Background()
	repo := newOpenAIOAuthRuntimeSettingRepo()
	a, b := &SettingService{settingRepo: repo}, &SettingService{settingRepo: repo}
	yes := true
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := a.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, &yes, nil, nil, nil, nil)
		errors <- err
	}()
	go func() {
		defer wg.Done()
		_, err := b.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, &yes)
		errors <- err
	}()
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	raw, err := repo.GetValue(ctx, SettingKeyOpenAIOAuthRuntimeSettings)
	require.NoError(t, err)
	var settings OpenAIOAuthRuntimeSettings
	require.NoError(t, json.Unmarshal([]byte(raw), &settings))
	require.True(t, settings.OpenAIRateLimitSameAccountRetryEnabled)
	require.True(t, settings.CodexFingerprintEnhancementEnabled)
	require.False(t, settings.OpenAIRateLimitProxyRotationEnabled)
}

func TestCodexEnhancementUsesRuntimeDeviceModeAndRestoresSavedModes(t *testing.T) {
	ctx := context.Background()
	repo := newOpenAIOAuthRuntimeSettingRepo()
	settings := &SettingService{settingRepo: repo}
	gateway := &OpenAIGatewayService{settingService: settings}
	yes, no := true, false
	_, err := settings.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, &yes)
	require.NoError(t, err)
	for _, mode := range []string{"off", "device", "session", "full", "account_device"} {
		t.Run(mode, func(t *testing.T) {
			account := newTestOAuthAccount(42, map[string]any{codexFingerprintModeExtraKey: mode})
			before, _ := json.Marshal(account.Extra)
			prepared := gateway.prepareCodexFingerprintAccount(ctx, account)
			require.True(t, codexFingerprintConvergenceEnabled(prepared))
			require.Equal(t, codexFingerprintDevice, activeCodexFingerprintMode(prepared))
			after, _ := json.Marshal(account.Extra)
			require.Equal(t, string(before), string(after))
			require.False(t, account.codexPolicyPrepared)
			_, err = settings.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, &no)
			require.NoError(t, err)
			restored := gateway.prepareCodexFingerprintAccount(ctx, account)
			require.False(t, codexFingerprintConvergenceEnabled(restored))
			require.Equal(t, codexFingerprintMode(mode), activeCodexFingerprintMode(restored))
			// A running attempt does not change underneath the caller.
			require.True(t, codexFingerprintConvergenceEnabled(prepared))
			_, err = settings.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, &yes)
			require.NoError(t, err)
		})
	}
}

func TestCodexEnhancementIgnoresAccountExtraWhenGlobalSwitchIsOff(t *testing.T) {
	gateway := &OpenAIGatewayService{}
	account := newTestOAuthAccount(42, map[string]any{codexFingerprintConvergenceExtraKey: true})
	account.codexPolicyPrepared = false
	prepared := gateway.prepareCodexFingerprintAccount(context.Background(), account)
	require.False(t, codexFingerprintConvergenceEnabled(prepared))
	require.True(t, codexFingerprintConvergenceEnabled(account))
}

func TestCodexEnhancementLeavesAPIKeyAndSetupTokenOnLegacyPolicy(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	settings := &SettingService{settingRepo: repo}
	yes := true
	_, err := settings.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, nil, nil, nil, nil, &yes)
	require.NoError(t, err)
	gateway := &OpenAIGatewayService{settingService: settings}
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeSetupToken} {
		account := &Account{ID: 42, Platform: PlatformOpenAI, Type: kind, Extra: map[string]any{codexFingerprintConvergenceExtraKey: true}}
		prepared := gateway.prepareCodexFingerprintAccount(context.Background(), account)
		require.False(t, prepared.codexFingerprintEnhanced)
		require.False(t, codexFingerprintConvergenceEnabled(prepared))
	}
}

func TestCodexWSExistingAttemptKeepsSnapshotButNextTurnRequiresReconnect(t *testing.T) {
	ctx := context.Background()
	settings := &SettingService{settingRepo: newOpenAIOAuthRuntimeSettingRepo()}
	gateway := &OpenAIGatewayService{settingService: settings}
	a := newTestOAuthAccount(42, map[string]any{codexFingerprintModeExtraKey: "session"})
	running := gateway.prepareCodexFingerprintAccount(ctx, a)
	require.NoError(t, gateway.checkCodexWSAttemptConfiguration(ctx, running))
	yes := true
	_, err := settings.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, &yes)
	require.NoError(t, err)
	require.False(t, running.codexFingerprintEnhanced)
	require.ErrorContains(t, gateway.checkCodexWSAttemptConfiguration(ctx, running), "reconnect")
	next := gateway.prepareCodexFingerprintAccount(ctx, a)
	require.True(t, next.codexFingerprintEnhanced)
	require.NoError(t, gateway.checkCodexWSAttemptConfiguration(ctx, next))
}

type codexDelayedRuntimeRepo struct {
	*openAIOAuthRuntimeSettingRepo
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (r *codexDelayedRuntimeRepo) GetValue(ctx context.Context, key string) (string, error) {
	value, err := r.openAIOAuthRuntimeSettingRepo.GetValue(ctx, key)
	if r.calls.Add(1) == 1 {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return value, err
}
func TestCodexEnhancementLateCacheReadCannotUndoSavedPolicy(t *testing.T) {
	repo := &codexDelayedRuntimeRepo{openAIOAuthRuntimeSettingRepo: newOpenAIOAuthRuntimeSettingRepo(), started: make(chan struct{}), release: make(chan struct{})}
	svc := &SettingService{settingRepo: repo}
	result := make(chan *OpenAIOAuthRuntimeSettings, 1)
	go func() { result <- svc.GetOpenAIOAuthRuntimeSettings(context.Background()) }()
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	yes := true
	_, err := svc.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, nil, nil, nil, nil, &yes)
	require.NoError(t, err)
	close(repo.release)
	select {
	case got := <-result:
		require.True(t, got.CodexFingerprintEnhancementEnabled)
	case <-time.After(time.Second):
		t.Fatal("read did not finish")
	}
	require.True(t, svc.GetOpenAIOAuthRuntimeSettings(context.Background()).CodexFingerprintEnhancementEnabled)
}
func TestCodexWSCompatibilitySeparatesEnhancementEvenWithIdenticalHeaders(t *testing.T) {
	account := newTestOAuthAccount(42, map[string]any{codexFingerprintModeExtraKey: "device"})
	before := normalizeOpenAIWSRequestCompatibility(openAIWSAcquireRequest{Account: account})
	after := snapshotOpenAIOutboundAccount(account)
	after.codexFingerprintEnhanced = true
	require.NotEqual(t, before, normalizeOpenAIWSRequestCompatibility(openAIWSAcquireRequest{Account: after}))
}
