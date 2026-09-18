//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateAtomicUpdatesAndAdminEdit(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountRepository(integrationEntClient, integrationDB, nil).(*accountRepository)
	a := &service.Account{Name: "state-integration", Platform: "openai", Type: "oauth", Credentials: map[string]any{"chatgpt_account_id": "owner-a", "access_token": "initial"}, Extra: map[string]any{}, Status: "active", Schedulable: true, Concurrency: 2, Priority: 1}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DELETE FROM scheduler_outbox WHERE account_id=$1", a.ID)
		_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", a.ID)
	})
	var outboxBefore int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", a.ID).Scan(&outboxBefore))
	stale, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	var wg sync.WaitGroup
	failures := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- repo.MutateCodexTurnState(ctx, a.ID, func(latest *service.Account) (map[string]any, error) {
				n, _ := latest.Extra[service.CodexTurnStatePoolKey].(float64)
				return map[string]any{service.CodexTurnStatePoolKey: n + 1}, nil
			})
		}()
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		require.NoError(t, e)
	}
	var outboxAfter int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", a.ID).Scan(&outboxAfter))
	require.Equal(t, outboxBefore, outboxAfter, "candidate updates must not enqueue scheduler rebuilds")
	current, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, float64(24), current.Extra[service.CodexTurnStatePoolKey])
	// A stale full-object admin edit must preserve the runtime value from the locked row.
	stale.Name = "edited"
	stale.Credentials["access_token"] = "refreshed"
	require.NoError(t, repo.Update(ctx, stale))
	current, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, float64(24), current.Extra[service.CodexTurnStatePoolKey])
	require.True(t, current.Schedulable)
	err = repo.MutateCodexTurnState(ctx, a.ID, func(*service.Account) (map[string]any, error) { return map[string]any{"unrelated": "bad"}, nil })
	require.Error(t, err)
	current, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.NotContains(t, current.Extra, "unrelated")
	// A held row lock must respect caller timeout, not block forwarding indefinitely.
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec("SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE", a.ID)
	require.NoError(t, err)
	deadline, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	err = repo.MutateCodexTurnState(deadline, a.ID, func(*service.Account) (map[string]any, error) { return nil, nil })
	require.Error(t, err)
}

func TestCodexTurnStateEncryptedStoreAndImportProtection(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountRepository(integrationEntClient, integrationDB, nil).(*accountRepository)
	encryptor, err := NewAESEncryptor(&config.Config{Totp: config.TotpConfig{EncryptionKey: strings.Repeat("42", 32)}})
	require.NoError(t, err)
	token := "private-state-" + fmt.Sprint(time.Now().UnixNano())
	sealed, err := encryptor.Encrypt(token)
	require.NoError(t, err)
	a := &service.Account{Name: "state-encrypted", Platform: "openai", Type: "oauth", Credentials: map[string]any{}, Extra: map[string]any{service.CodexTurnStatePoolKey: "client-injected"}, Status: "active", Schedulable: true, Concurrency: 1, Priority: 1}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DELETE FROM scheduler_outbox WHERE account_id=$1", a.ID)
		_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", a.ID)
	})
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.NotContains(t, fresh.Extra, service.CodexTurnStatePoolKey)
	require.NoError(t, repo.MutateCodexTurnState(ctx, a.ID, func(*service.Account) (map[string]any, error) {
		return map[string]any{service.CodexTurnStatePoolKey: sealed}, nil
	}))
	var data string
	require.NoError(t, integrationDB.QueryRow("SELECT extra::text FROM accounts WHERE id=$1", a.ID).Scan(&data))
	require.NotContains(t, data, token)
	require.NotContains(t, service.CodexTurnStatePublicExtra(a), service.CodexTurnStatePoolKey)
	plaintext, err := encryptor.Decrypt(sealed)
	require.NoError(t, err)
	require.Equal(t, token, plaintext)
}
