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
	err   error
	reads int
}

func (r *openAIOAuthNewAccountDefaultsRepo) GetValue(context.Context, string) (string, error) {
	r.reads++
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
	require.Equal(t, 1, repo.reads)
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
