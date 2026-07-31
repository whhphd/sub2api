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
}

func (r *openAIOAuthNewAccountDefaultsRepo) GetValue(context.Context, string) (string, error) {
	return r.value, r.err
}

func TestApplyOpenAIOAuthNewAccountDefaults(t *testing.T) {
	t.Run("enabled overrides false and preserves threshold", func(t *testing.T) {
		svc := NewSettingService(&openAIOAuthNewAccountDefaultsRepo{value: "true"}, nil)
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

		require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
		require.Equal(t, true, account.Extra[openAIOAuthInjectNoopToolCallExtraKey])
		require.Equal(t, true, account.Extra[openAIOAuthInjectNoopToolCallIgnore429CooldownExtraKey])
		require.Equal(t, float64(17), account.Extra[openAIOAuth429ConsecutiveThresholdExtraKey])
		require.Equal(t, true, account.Extra["preserved"])
	})

	t.Run("disabled leaves account unchanged", func(t *testing.T) {
		svc := NewSettingService(&openAIOAuthNewAccountDefaultsRepo{value: "false"}, nil)
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"preserved": true}}

		require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
		require.Equal(t, map[string]any{"preserved": true}, account.Extra)
	})

	t.Run("missing setting defaults disabled", func(t *testing.T) {
		svc := NewSettingService(&openAIOAuthNewAccountDefaultsRepo{err: ErrSettingNotFound}, nil)
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

		require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
		require.Nil(t, account.Extra)
	})

	t.Run("non OpenAI OAuth does not read setting", func(t *testing.T) {
		svc := NewSettingService(&openAIOAuthNewAccountDefaultsRepo{err: errors.New("unexpected read")}, nil)
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

		require.NoError(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account))
	})

	t.Run("read error blocks creation", func(t *testing.T) {
		svc := NewSettingService(&openAIOAuthNewAccountDefaultsRepo{err: errors.New("database unavailable")}, nil)
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

		require.ErrorContains(t, svc.ApplyOpenAIOAuthNewAccountDefaults(context.Background(), account), "database unavailable")
	})
}
