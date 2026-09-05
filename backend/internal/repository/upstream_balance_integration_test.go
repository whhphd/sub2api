//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpstreamBalancePersistenceProtectsNewerState(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	a := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "balance-persist", Platform: "grok", Type: "apikey",
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://relay.example"},
		Extra:       map[string]any{"quota_limit": 77.0},
	})
	stale, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	value := 12.0
	snapshot := &service.UpstreamBalanceSnapshot{Status: "ok", Scope: "wallet", Balance: &value, LastAttemptAt: time.Now().UTC()}
	require.NoError(t, repo.UpdateUpstreamBalanceSnapshot(ctx, stale, snapshot))
	require.ErrorIs(t, repo.UpdateUpstreamBalanceSnapshot(ctx, stale, snapshot), service.ErrUpstreamBalanceIdentityChanged)
	stale.Name = "renamed"
	require.NoError(t, repo.Update(ctx, stale))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, value, *service.UpstreamBalanceFromAccount(got).Balance)
	require.Equal(t, 77.0, got.Extra["quota_limit"])
	old, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	got.Credentials["api_key"] = "new-key"
	require.NoError(t, repo.Update(ctx, got))
	changed, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Nil(t, service.UpstreamBalanceFromAccount(changed))
	require.ErrorIs(t, repo.UpdateUpstreamBalanceSnapshot(ctx, old, snapshot), service.ErrUpstreamBalanceIdentityChanged)
}

func TestUpstreamBalanceDueSelectionIsDefaultAndBounded(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	_, err := tx.ExecContext(ctx, `UPDATE accounts SET status = 'inactive'`)
	require.NoError(t, err)
	now := time.Now().UTC()
	create := func(name, kind string, extra map[string]any) int64 {
		return mustCreateAccount(t, tx.Client(), &service.Account{Name: name, Platform: "anthropic", Type: kind, Status: "active", Extra: extra}).ID
	}
	create("not-due-balance", "apikey", map[string]any{service.UpstreamBalanceExtraKey: map[string]any{"next_query_at": now.Add(time.Hour).Format(time.RFC3339Nano)}})
	create("oauth-balance-excluded", "oauth", nil)
	id := create("unqueried-balance", "apikey", nil)
	create("malformed-balance", "apikey", map[string]any{service.UpstreamBalanceExtraKey: map[string]any{"next_query_at": "2026-99-99T00:00:00Z"}})
	rows, err := repo.ListDueUpstreamBalanceAccounts(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, id, rows[0].ID)
	rows, err = repo.ListDueUpstreamBalanceAccounts(ctx, now, 12)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}
