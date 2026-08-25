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

	t.Run("exhausted quota headers override transient body", func(t *testing.T) {
		svc := newOpenAI429SameAccountRetryTestService(t, true)
		headers := http.Header{}
		headers.Set("x-codex-primary-used-percent", "100")
		headers.Set("x-codex-primary-reset-after-seconds", "18000")
		headers.Set("x-codex-primary-window-minutes", "300")
		err := &UpstreamFailoverError{
			StatusCode:      http.StatusTooManyRequests,
			ResponseHeaders: headers,
			ResponseBody:    []byte(`{"detail":"Rate limit exceeded"}`),
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
