package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAIOAuthNewAccountDefaultsRepo struct {
	SettingRepository
	value string
	mode  string
	err   error
	reads int
}

func (r *openAIOAuthNewAccountDefaultsRepo) GetValue(_ context.Context, key string) (string, error) {
	r.reads++
	if key == SettingKeyOpenAIOAuthDefaultCodexFingerprintMode {
		return r.mode, r.err
	}
	return r.value, r.err
}

func TestApplyOpenAIOAuthNewAccountDefaultsDefaultsToSession(t *testing.T) {
	repo := &openAIOAuthNewAccountDefaultsRepo{value: "true"}
	svc := NewSettingService(repo, nil)
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"preserved": true},
	}

	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
	require.Equal(t, string(codexFingerprintSession), account.Extra[codexFingerprintModeExtraKey])
	require.Equal(t, 2, repo.reads)
	require.NotNil(t, account.Extra["preserved"])
	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), nil))
}

func TestApplyOpenAIOAuthNewAccountDefaultsCanBeDisabled(t *testing.T) {
	repo := &openAIOAuthNewAccountDefaultsRepo{value: "false"}
	svc := NewSettingService(repo, nil)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
	require.NotContains(t, account.Extra, codexFingerprintModeExtraKey)
}

func TestApplyOpenAIOAuthNewAccountDefaultsPreservesExplicitMode(t *testing.T) {
	repo := &openAIOAuthNewAccountDefaultsRepo{value: "true"}
	svc := NewSettingService(repo, nil)
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintOff)},
	}
	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
	require.Equal(t, string(codexFingerprintOff), account.Extra[codexFingerprintModeExtraKey])
	require.Zero(t, repo.reads)

	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), apiKey))
	require.Nil(t, apiKey.Extra)

}

func TestApplyOpenAIOAuthNewAccountDefaultsReadErrorFailsOpen(t *testing.T) {
	repo := &openAIOAuthNewAccountDefaultsRepo{err: errors.New("setting unavailable")}
	svc := NewSettingService(repo, nil)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
	require.Equal(t, string(codexFingerprintSession), account.Extra[codexFingerprintModeExtraKey])
}

func TestApplyOpenAIOAuthNewAccountDefaultsUsesConfiguredMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
	}{
		{name: "off", mode: string(codexFingerprintOff)},
		{name: "device", mode: string(codexFingerprintDevice)},
		{name: "session", mode: string(codexFingerprintSession)},
		{name: "full", mode: string(codexFingerprintFull)},
		{name: "invalid falls back", mode: "unexpected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &openAIOAuthNewAccountDefaultsRepo{value: "true", mode: tc.mode}
			svc := NewSettingService(repo, nil)
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

			require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
			expected := tc.mode
			if tc.name == "invalid falls back" {
				expected = string(codexFingerprintSession)
			}
			require.Equal(t, expected, account.Extra[codexFingerprintModeExtraKey])
		})
	}
}
