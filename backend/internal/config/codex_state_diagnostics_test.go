package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCodexDiagnosticsConfig(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.Gateway.CodexStateDiagnostics.Enabled)
	require.Equal(t, 10, cfg.Gateway.CodexStateDiagnostics.SamplePercent)
	require.Empty(t, cfg.Gateway.CodexStateDiagnostics.AccountIDs)
	for _, cfg := range []CodexStateDiagnosticsConfig{
		{Enabled: true, SamplePercent: 10}, {Enabled: true, AccountIDs: []int64{42}},
		{SamplePercent: 101}, {SamplePercent: -1}, {AccountIDs: []int64{0}},
	} {
		require.Error(t, cfg.Validate())
	}
	require.NoError(t, (CodexStateDiagnosticsConfig{Enabled: true, AccountIDs: []int64{42}, SamplePercent: 100}).Validate())
}

func TestCodexDiagnosticsEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("GATEWAY_CODEX_STATE_DIAGNOSTICS_ENABLED", "true")
	t.Setenv("GATEWAY_CODEX_STATE_DIAGNOSTICS_ACCOUNT_IDS", "42,43")
	t.Setenv("GATEWAY_CODEX_STATE_DIAGNOSTICS_SAMPLE_PERCENT", "100")
	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.Gateway.CodexStateDiagnostics.Enabled)
	require.Equal(t, []int64{42, 43}, cfg.Gateway.CodexStateDiagnostics.AccountIDs)
}
