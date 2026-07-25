package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewOpenAIAccountUpstreamFailoverError_NoOp429PolicyDoesNotForceSameAccountRetry(t *testing.T) {
	enabledAccount := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAIOAuthInjectNoopToolCallExtraKey:                  true,
			openAIOAuthInjectNoopToolCallIgnore429CooldownExtraKey: true,
		},
	}

	t.Run("enabled 429 keeps the normal failover policy", func(t *testing.T) {
		failoverErr := newOpenAIAccountUpstreamFailoverError(
			enabledAccount,
			http.StatusTooManyRequests,
			http.Header{},
			nil,
			"rate limited",
			false,
		)

		require.False(t, failoverErr.RetryableOnSameAccount)
		require.Zero(t, failoverErr.SameAccountRetryLimit)
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
		require.False(t, failoverErr.RetryableOnSameAccount)
		require.Zero(t, failoverErr.SameAccountRetryLimit)
	})
}
