//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCodexExitSnapshotConditionalPersistence(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	proxy := mustCreateProxy(t, tx.Client(), &service.Proxy{Name: "codex-exit-proxy"})
	a := mustCreateAccount(t, tx.Client(), &service.Account{Name: "codex-exit-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ProxyID: &proxy.ID, Extra: map[string]any{"unrelated": "keep", "codex_fingerprint_mode": "session"}})
	first, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	updates := map[string]any{"codex_wire_timezone_resolved": "America/Toronto", "codex_wire_timezone_resolved_at": "2026-09-16T00:00:00Z"}
	applied, err := repo.UpdateCodexExitSnapshot(ctx, first, updates)
	require.NoError(t, err)
	require.True(t, applied)
	applied, err = repo.UpdateCodexExitSnapshot(ctx, first, map[string]any{"codex_wire_timezone_resolved": "Asia/Tokyo", "codex_wire_timezone_resolved_at": "2026-09-15T00:00:00Z"})
	require.NoError(t, err)
	require.False(t, applied)
	current, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "keep", current.Extra["unrelated"])
	require.Equal(t, "session", current.Extra["codex_fingerprint_mode"])
	require.Equal(t, "America/Toronto", current.Extra["codex_wire_timezone_resolved"])
	require.NoError(t, tx.Client().Proxy.UpdateOneID(proxy.ID).SetHost("changed.invalid").Exec(ctx))
	applied, err = repo.UpdateCodexExitSnapshot(ctx, current, updates)
	require.NoError(t, err)
	require.False(t, applied)
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Client().Account.UpdateOneID(a.ID).ClearProxyID().Exec(ctx))
	applied, err = repo.UpdateCodexExitSnapshot(ctx, fresh, updates)
	require.NoError(t, err)
	require.False(t, applied)
}

func TestRuntimeSettingCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	r := NewSettingRepository(tx.Client()).(*settingRepository)
	changed, err := r.CompareAndSwap(ctx, "codex-runtime-cas", "", "first")
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = r.CompareAndSwap(ctx, "codex-runtime-cas", "", "stale")
	require.NoError(t, err)
	require.False(t, changed)
	changed, err = r.CompareAndSwap(ctx, "codex-runtime-cas", "first", "second")
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = r.CompareAndSwap(ctx, "codex-runtime-cas", "first", "stale")
	require.NoError(t, err)
	require.False(t, changed)
	got, err := r.GetValue(ctx, "codex-runtime-cas")
	require.NoError(t, err)
	require.Equal(t, "second", got)
}
