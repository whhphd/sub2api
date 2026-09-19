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
	// Real credential faults are independent of the model hold. Releasing it retains the fault.
	require.NoError(t, repo.SetTempUnschedulable(ctx, b.ID, time.Now().Add(time.Hour), "credential_rejected"))
	ok, err = repo.CompareAndSwapTurnStateHold(ctx, stale, nil, "turn_state_hold:gpt-test")
	require.NoError(t, err)
	require.True(t, ok)
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

func TestTurnStateModelHoldsMigrateLegacyAndPreserveQuota(t *testing.T){
 ctx:=context.Background();repo:=NewAccountRepository(integrationEntClient,integrationDB,nil).(*accountRepository)
 _,e:=integrationDB.Exec(`INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,service.SettingKeyOpenAIOAuthRuntimeSettings,`{"openai_oauth_turn_state_auto_enabled":true,"openai_oauth_turn_state_hunter":{"enabled":true,"hold_when_degraded":true}}`);require.NoError(t,e)
 a:=&service.Account{Name:"model-hold",Platform:"openai",Type:"oauth",Credentials:map[string]any{"chatgpt_account_id":"offline"},Extra:map[string]any{},Status:"active",Schedulable:true,Concurrency:1};require.NoError(t,repo.Create(ctx,a))
 t.Cleanup(func(){_,_=integrationDB.Exec("DELETE FROM scheduler_outbox WHERE account_id=$1",a.ID);_,_=integrationDB.Exec("DELETE FROM accounts WHERE id=$1",a.ID);_,_=integrationDB.Exec("DELETE FROM settings WHERE key=$1",service.SettingKeyOpenAIOAuthRuntimeSettings)})
 until:=time.Now().Add(time.Hour);require.NoError(t,repo.SetTempUnschedulable(ctx,a.ID,until,"turn_state_hold:gpt-a"));old,e:=repo.GetByID(ctx,a.ID);require.NoError(t,e)
 ok,e:=repo.CompareAndSwapTurnStateHold(ctx,old,&until,"turn_state_hold:gpt-a");require.NoError(t,e);require.True(t,ok)
 v,e:=repo.GetByID(ctx,a.ID);require.NoError(t,e);require.Empty(t,v.TempUnschedulableReason);require.True(t,v.IsSchedulable());require.False(t,v.IsSchedulableForModel("gpt-a"));require.True(t,v.IsSchedulableForModel("gpt-b"))
 projected:=filterSchedulerExtra(v.Extra);require.Contains(t,projected,service.CodexTurnStateModelHoldsKey)
 require.NoError(t,repo.SetModelRateLimit(ctx,a.ID,"gpt-b",until,"real_quota"));v,e=repo.GetByID(ctx,a.ID);require.NoError(t,e)
 // Different model writes based on the same snapshot must both survive.
 ok,e=repo.CompareAndSwapTurnStateHold(ctx,v,&until,"turn_state_hold:gpt-c");require.NoError(t,e);require.True(t,ok)
 v.Name="edited";require.NoError(t,repo.Update(ctx,v));v,e=repo.GetByID(ctx,a.ID);require.NoError(t,e);holds,ok:=v.Extra[service.CodexTurnStateModelHoldsKey].(map[string]any);require.True(t,ok);require.Len(t,holds,2)
 _,e=integrationDB.Exec(`UPDATE settings SET value=$2 WHERE key=$1`,service.SettingKeyOpenAIOAuthRuntimeSettings,`{"openai_oauth_turn_state_hunter":{"hold_when_degraded":false}}`);require.NoError(t,e)
 n,e:=repo.ReleaseTurnStateHoldsIfDisabled(ctx);require.NoError(t,e);require.Equal(t,1,n)
 v,e=repo.GetByID(ctx,a.ID);require.NoError(t,e);require.True(t,v.IsSchedulableForModel("gpt-a"));require.True(t,v.IsSchedulableForModel("gpt-c"));require.False(t,v.IsSchedulableForModel("gpt-b"))
}
