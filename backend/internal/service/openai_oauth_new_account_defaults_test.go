package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIOAuthNewAccountDefaultsRepo struct {
	SettingRepository
	value            string
	mode             string
	err              error
	reads            int
	proxyPoolEnabled string
	proxyPoolIDs     string
}

func (r *openAIOAuthNewAccountDefaultsRepo) GetValue(_ context.Context, key string) (string, error) {
	r.reads++
	if key == SettingKeyOpenAIOAuthDefaultCodexFingerprintMode {
		return r.mode, r.err
	}
	return r.value, r.err
}

func (r *openAIOAuthNewAccountDefaultsRepo) GetMultiple(_ context.Context, _ []string) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	return map[string]string{
		SettingKeyOpenAIOAuthNewAccountProxyPoolEnabled: r.proxyPoolEnabled,
		SettingKeyOpenAIOAuthNewAccountProxyPoolIDs:     r.proxyPoolIDs,
	}, nil
}

type openAIOAuthNewAccountProxyRepo struct {
	ProxyRepository
	proxies []Proxy
	err     error
	reads   int
}

func (r *openAIOAuthNewAccountProxyRepo) ListByIDs(_ context.Context, _ []int64) ([]Proxy, error) {
	r.reads++
	return r.proxies, r.err
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

func TestApplyOpenAIOAuthNewAccountDefaultsAssignsRandomActiveProxy(t *testing.T) {
	expiredAt := time.Now().Add(-time.Minute)
	settings := &openAIOAuthNewAccountDefaultsRepo{
		value:            "true",
		mode:             string(codexFingerprintSession),
		proxyPoolEnabled: "true",
		proxyPoolIDs:     `[11,22,33,44,22,-1]`,
	}
	proxies := &openAIOAuthNewAccountProxyRepo{proxies: []Proxy{
		{ID: 11, Status: StatusActive},
		{ID: 22, Status: StatusActive},
		{ID: 33, Status: "inactive"},
		{ID: 44, Status: StatusActive, ExpiresAt: &expiredAt},
	}}
	svc := NewSettingService(settings, nil)
	svc.SetProxyRepository(proxies)
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintOff)},
	}

	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
	require.NotNil(t, account.ProxyID)
	require.Contains(t, []int64{11, 22}, *account.ProxyID)
	require.Equal(t, 1, proxies.reads)
	require.Equal(t, string(codexFingerprintOff), account.Extra[codexFingerprintModeExtraKey])
}

func TestApplyOpenAIOAuthNewAccountDefaultsPreservesExplicitProxy(t *testing.T) {
	explicitProxyID := int64(99)
	settings := &openAIOAuthNewAccountDefaultsRepo{
		value:            "true",
		proxyPoolEnabled: "true",
		proxyPoolIDs:     `[11]`,
	}
	proxies := &openAIOAuthNewAccountProxyRepo{proxies: []Proxy{{ID: 11, Status: StatusActive}}}
	svc := NewSettingService(settings, nil)
	svc.SetProxyRepository(proxies)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &explicitProxyID}

	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
	require.Equal(t, explicitProxyID, *account.ProxyID)
	require.Zero(t, proxies.reads)
}

func TestParseOpenAIOAuthNewAccountProxyPoolIDs(t *testing.T) {
	require.Equal(t, []int64{2, 3}, ParseOpenAIOAuthNewAccountProxyPoolIDs(`[3,2,3,0,-1]`))
	require.Empty(t, ParseOpenAIOAuthNewAccountProxyPoolIDs(`not-json`))
}
