package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewOpenAIAccountUpstreamFailoverError_NoOp429RetryPolicy(t *testing.T) {
	enabledAccount := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAIOAuthInjectNoopToolCallExtraKey:                  true,
			openAIOAuthInjectNoopToolCallIgnore429CooldownExtraKey: true,
		},
	}

	t.Run("enabled 429 forces three same-account retries", func(t *testing.T) {
		failoverErr := newOpenAIAccountUpstreamFailoverError(
			enabledAccount,
			http.StatusTooManyRequests,
			http.Header{},
			nil,
			"rate limited",
			false,
		)

		require.True(t, failoverErr.RetryableOnSameAccount)
		require.Equal(t, openAIOAuthNoop429SameAccountRetryLimit, failoverErr.SameAccountRetryLimit)
		require.Equal(t, openAIOAuthNoop429SameAccountRetryLimit, failoverErr.EffectiveSameAccountRetryLimit(10))
	})

	t.Run("non-429 keeps existing policy", func(t *testing.T) {
		failoverErr := newOpenAIAccountUpstreamFailoverError(
			enabledAccount,
			http.StatusBadGateway,
			http.Header{},
			nil,
			"bad gateway",
			false,
		)

		require.False(t, failoverErr.RetryableOnSameAccount)
		require.Zero(t, failoverErr.SameAccountRetryLimit)
		require.Equal(t, 10, failoverErr.EffectiveSameAccountRetryLimit(10))
	})

	t.Run("child without parent keeps existing policy", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Extra: map[string]any{
				openAIOAuthInjectNoopToolCallIgnore429CooldownExtraKey: true,
			},
		}
		failoverErr := newOpenAIAccountUpstreamFailoverError(
			account,
			http.StatusTooManyRequests,
			http.Header{},
			nil,
			"rate limited",
			false,
		)

		require.False(t, failoverErr.RetryableOnSameAccount)
		require.Zero(t, failoverErr.SameAccountRetryLimit)
	})
}
