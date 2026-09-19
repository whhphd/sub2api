//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestTurnStateHoldImmediateReleaseAndFaultIsolation(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountRepository(integrationEntClient, integrationDB, nil).(*accountRepository)
	_, err := integrationDB.Exec(`INSERT INTO settings(key,value) VALUES ($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, service.SettingKeyOpenAIOAuthRuntimeSettings, `{"openai_oauth_turn_state_auto_enabled":true,"openai_oauth_turn_state_hunter":{"enabled":true,"hold_when_degraded":true}}`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DELETE FROM settings WHERE key=$1", service.SettingKeyOpenAIOAuthRuntimeSettings)
	})
	makeAccount := func() *service.Account {
		a := &service.Account{Name: "hold-integration", Platform: "openai", Type: "oauth", Credentials: map[string]any{"chatgpt_account_id": "offline-owner"}, Extra: map[string]any{}, Status: "active", Schedulable: true, Concurrency: 1}
		require.NoError(t, repo.Create(ctx, a))
		t.Cleanup(func() {
			_, _ = integrationDB.Exec("DELETE FROM scheduler_outbox WHERE account_id=$1", a.ID)
			_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", a.ID)
		})
		v, e := repo.GetByID(ctx, a.ID)
		require.NoError(t, e)
		return v
	}
	a, b := makeAccount(), makeAccount()
	until := time.Now().Add(24 * time.Hour)
	ok, err := repo.CompareAndSwapTurnStateHold(ctx, a, &until, "turn_state_hold:gpt-test")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.CompareAndSwapTurnStateHold(ctx, b, &until, "turn_state_hold:gpt-test")
	require.NoError(t, err)
	require.True(t, ok)
	stale, err := repo.GetByID(ctx, b.ID)
	require.NoError(t, err)
	// A later, shorter real credential fault supersedes the long hunter hold.
	require.NoError(t, repo.SetTempUnschedulable(ctx, b.ID, time.Now().Add(time.Hour), "credential_rejected"))
	ok, err = repo.CompareAndSwapTurnStateHold(ctx, stale, nil, "")
	require.NoError(t, err)
	require.False(t, ok)
	_, err = integrationDB.Exec(`UPDATE accounts SET rate_limit_reset_at=NOW()+INTERVAL '1 hour' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE settings SET value=$2 WHERE key=$1`, service.SettingKeyOpenAIOAuthRuntimeSettings, `{"openai_oauth_turn_state_auto_enabled":true,"openai_oauth_turn_state_hunter":{"enabled":true,"hold_when_degraded":false}}`)
	require.NoError(t, err)
	count, err := repo.ReleaseTurnStateHoldsIfDisabled(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, fresh.TempUnschedulableReason)
	require.NotNil(t, fresh.RateLimitResetAt)
	fresh, err = repo.GetByID(ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, "credential_rejected", fresh.TempUnschedulableReason)
	// A request that read ON before the save must not re-insert a hold afterward.
	ok, err = repo.CompareAndSwapTurnStateHold(ctx, a, &until, "turn_state_hold:gpt-test")
	require.NoError(t, err)
	require.False(t, ok)
	count, err = repo.ReleaseTurnStateHoldsIfDisabled(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}
