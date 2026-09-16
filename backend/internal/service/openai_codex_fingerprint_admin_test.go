//go:build unit

// Adapted from LuckyKuang/sub2api-plus 1f06d5871f (LGPL-3.0).
package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAdminServiceUpdateAccountPreservesExplicitCodexFingerprintModeWhenOmitted(t *testing.T) {
	repo := &longContextBillingRepoStub{account: &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			codexFingerprintModeExtraKey: "session",
			"unrelated":                  true,
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{
		Extra: map[string]any{"unrelated": false},
	})

	require.NoError(t, err)
	require.Equal(t, "session", account.Extra[codexFingerprintModeExtraKey])
	require.Equal(t, false, account.Extra["unrelated"])
}

func TestAccountServiceUpdatePreservesExplicitCodexFingerprintModeWhenReplacementOmitsField(t *testing.T) {
	repo := &longContextBillingRepoStub{account: &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			codexFingerprintModeExtraKey: "session",
			"unrelated":                  true,
		},
	}}
	svc := NewAccountService(repo, nil)
	replacement := map[string]any{"unrelated": false}

	account, err := svc.Update(context.Background(), 1, UpdateAccountRequest{Extra: &replacement})

	require.NoError(t, err)
	require.Equal(t, "session", account.Extra[codexFingerprintModeExtraKey])
	require.Equal(t, false, account.Extra["unrelated"])
}
