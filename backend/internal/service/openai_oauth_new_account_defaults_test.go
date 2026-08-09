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

func TestApplyOpenAIOAuthNewAccountDefaultsIsCompatibilityNoop(t *testing.T) {
	repo := &openAIOAuthNewAccountDefaultsRepo{value: "true", err: errors.New("must not read legacy setting")}
	svc := NewSettingService(repo, nil)
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAIOAuthInjectNoopToolCallExtraKey:                  false,
			openAIOAuthInjectNoopToolCallIgnore429CooldownExtraKey: false,
			openAIOAuth429ConsecutiveThresholdExtraKey:             float64(17),
			"preserved": true,
		},
	}
	want := make(map[string]any, len(account.Extra))
	for key, value := range account.Extra {
		want[key] = value
	}

	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
	require.Equal(t, want, account.Extra)
	require.Zero(t, repo.reads)
	require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), nil))
}
