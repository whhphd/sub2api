package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func newOpenAI429SameAccountRetryTestService(t *testing.T, enabled bool) *OpenAIGatewayService {
	t.Helper()
	repo := newOpenAIOAuthRuntimeSettingRepo()
	settings := DefaultOpenAIOAuthRuntimeSettings(false)
	settings.OpenAIRateLimitSameAccountRetryEnabled = enabled
	data, err := json.Marshal(settings)
	require.NoError(t, err)
	repo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(data)
	return &OpenAIGatewayService{settingService: NewSettingService(repo, nil)}
}

func TestApplyOpenAIOAuthRateLimitSameAccountRetryPolicy(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	shortRateLimit := &UpstreamFailoverError{
		StatusCode:   http.StatusTooManyRequests,
		ResponseBody: []byte(`{"detail":"Rate limit exceeded"}`),
	}

	t.Run("disabled keeps failover", func(t *testing.T) {
		svc := newOpenAI429SameAccountRetryTestService(t, false)
		svc.ApplyOpenAIOAuthRateLimitSameAccountRetryPolicy(context.Background(), account, shortRateLimit)
		require.False(t, shortRateLimit.RetryableOnSameAccount)
	})

	t.Run("enabled retries short rate limit on same account", func(t *testing.T) {
		svc := newOpenAI429SameAccountRetryTestService(t, true)
		err := &UpstreamFailoverError{
			StatusCode:   http.StatusTooManyRequests,
			ResponseBody: []byte(`{"detail":"Rate limit exceeded"}`),
		}
		svc.ApplyOpenAIOAuthRateLimitSameAccountRetryPolicy(context.Background(), account, err)
		require.True(t, err.RetryableOnSameAccount)
	})

	t.Run("usage exhaustion is excluded", func(t *testing.T) {
		svc := newOpenAI429SameAccountRetryTestService(t, true)
		err := &UpstreamFailoverError{
			StatusCode:   http.StatusTooManyRequests,
			ResponseBody: []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`),
		}
		svc.ApplyOpenAIOAuthRateLimitSameAccountRetryPolicy(context.Background(), account, err)
		require.False(t, err.RetryableOnSameAccount)
	})

	t.Run("non OAuth accounts are excluded", func(t *testing.T) {
		svc := newOpenAI429SameAccountRetryTestService(t, true)
		err := &UpstreamFailoverError{
			StatusCode:   http.StatusTooManyRequests,
			ResponseBody: []byte(`{"detail":"Rate limit exceeded"}`),
		}
		apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
		svc.ApplyOpenAIOAuthRateLimitSameAccountRetryPolicy(context.Background(), apiKey, err)
		require.False(t, err.RetryableOnSameAccount)
	})
}

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
