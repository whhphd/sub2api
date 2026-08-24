//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIsGrokContentPolicyRejection(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "new sensitive code",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"new_sensitive","message":"image is sensitive"}}`,
			want:   true,
		},
		{
			name:   "content policy violation code",
			status: http.StatusForbidden,
			body:   `{"response":{"error":{"code":"content_policy_violation"}}}`,
			want:   true,
		},
		{
			name:   "cyber policy code",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"cyber_policy","message":"request rejected"}}`,
			want:   true,
		},
		{
			name:   "moderation feature unavailable",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"The moderation feature is not available for this request"}}`,
			want:   true,
		},
		{
			name:   "explicit prompt moderation rejection",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"request rejected by content moderation"}}`,
			want:   true,
		},
		{
			name:   "entitlement forbidden",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"subscription required"}}`,
			want:   false,
		},
		{
			name:   "account policy suspension is not request policy",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"account suspended due to policy violation"}}`,
			want:   false,
		},
		{
			name:   "structured account suspension overrides policy reason",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"account_suspended","reason":"policy_violation","message":"account suspended due to policy violation"}}`,
			want:   false,
		},
		{
			name:   "ambiguous policy violation code is not enough",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"policy_violation","message":"policy violation"}}`,
			want:   false,
		},
		{
			name:   "policy violation with request scoped message",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"policy_violation","message":"request blocked by policy"}}`,
			want:   true,
		},
		{
			name:   "permission-denied usage guidelines is request scoped",
			status: http.StatusForbidden,
			body:   `{"code":"permission-denied","error":"Content violates usage guidelines. "}`,
			want:   true,
		},
		{
			name:   "permission-denied entitlement stays on the account path",
			status: http.StatusForbidden,
			body:   `{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`,
			want:   false,
		},
		{
			name:   "structured account code overrides usage guidelines phrase",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"account_suspended","message":"Content violates usage guidelines."}}`,
			want:   false,
		},
		{
			name:   "wrong status",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"new_sensitive"}}`,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokContentPolicyRejection(tt.status, []byte(tt.body)))
		})
	}
}

func TestGrokContentPolicy403DoesNotMutateOrFailover(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 4715, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)

	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, body))

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
	got := svc.failoverOpenAIUpstreamHTTPError(context.Background(), c, account, resp, body, "text is sensitive", "grok-4.5")
	require.Nil(t, got)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestGrokNonFailoverDoesNotApplyGenericTempUnschedulablePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, nil, nil, nil),
	}
	account := &Account{
		ID:       5099,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{map[string]any{
				"error_code":       float64(http.StatusForbidden),
				"keywords":         []any{"text is sensitive"},
				"duration_minutes": float64(1),
			}},
		},
	}
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}

	got := svc.failoverOpenAIUpstreamHTTPError(
		context.Background(), c, account, resp, body, "text is sensitive", "",
	)

	require.Nil(t, got)
	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestGrokContentPolicy403SharedErrorFallbackDoesNotMutate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"error":{"code":"content_filter","message":"prohibited content"}}`)
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       4719,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusTooManyRequests)},
		},
	}

	newContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		return c, recorder
	}

	c, recorder := newContext()
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
	_, err := svc.handleErrorResponse(context.Background(), resp, c, account, nil, "grok-4.5")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid_request_error")

	c, recorder = newContext()
	resp = &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
	_, err = svc.handleCompatErrorResponse(resp, c, account, writeChatCompletionsError, "grok-4.5")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid_request_error")

	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
}

func TestGrokContentPolicy403MediaResponseBypassesCustomErrorCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"error":{"code":"new_sensitive","message":"image is sensitive"}}`
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       4720,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusTooManyRequests)},
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	_, err := svc.handleGrokMediaErrorResponse(context.Background(), resp, c, account, "request-id", "grok-imagine")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid_request_error")
	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
}

func TestGrokContentPolicySSEErrorDoesNotMutateOrFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &grokQuotaAccountRepo{}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"error\",\"error\":{\"code\":\"new_sensitive\",\"message\":\"text is sensitive\"}}\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{accountRepo: repo, httpUpstream: upstream}
	account := &Account{ID: 4721, Platform: PlatformGrok, Type: AccountTypeOAuth, Concurrency: 1}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"grok-4.5","input":"hi"}`)
	var writes [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "access-token", payload, len(payload),
		"grok-4.5", "", "", "", "cache-id", 1,
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.Error(t, err)
	require.NotNil(t, result)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Len(t, writes, 1)
	require.Contains(t, string(writes[0]), "new_sensitive")
	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestGrokPermissionDeniedContentRefusalDoesNotMutateOrFailover(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 4785, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"code":"permission-denied","error":"Content violates usage guidelines. "}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)

	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, body))
}

func TestHandleGrokAccountUpstreamErrorEntitlement403KeepsDefaultCooldown(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 4716, Platform: PlatformGrok, Type: AccountTypeOAuth}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	require.Greater(t, repo.lastTempUnschedUntil, before.Add(29*time.Minute))
	require.Less(t, repo.lastTempUnschedUntil, before.Add(31*time.Minute))
}

func newGrokForbiddenRetryTestService(t *testing.T, accountRepo AccountRepository, enabled bool) *OpenAIGatewayService {
	t.Helper()
	settingRepo := newOpenAIOAuthRuntimeSettingRepo()
	settings := DefaultOpenAIOAuthRuntimeSettings(false)
	settings.GrokOAuthForbiddenSameAccountRetryEnabled = enabled
	data, err := json.Marshal(settings)
	require.NoError(t, err)
	settingRepo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(data)
	return &OpenAIGatewayService{
		accountRepo:    accountRepo,
		settingService: NewSettingService(settingRepo, nil),
	}
}

func TestGrokOAuthForbiddenSameAccountRetryPolicy(t *testing.T) {
	body := []byte(`{"error":{"message":"subscription required"}}`)
	account := &Account{ID: 4810, Name: "grok-oauth", Platform: PlatformGrok, Type: AccountTypeOAuth}

	t.Run("enabled retries three times and never temp unschedules", func(t *testing.T) {
		repo := &grokQuotaAccountRepo{}
		svc := newGrokForbiddenRetryTestService(t, repo, true)

		svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)
		require.Zero(t, repo.tempUnschedCalls)
		require.Zero(t, repo.rateLimitedCalls)

		failoverErr := svc.newGrokUpstreamFailoverError(
			context.Background(), account, http.StatusForbidden, http.Header{}, body,
			"subscription required", false,
		)
		require.True(t, failoverErr.RetryableOnSameAccount)
		require.Equal(t, 3, failoverErr.SameAccountRetryLimit)
		require.Equal(t, 3, failoverErr.EffectiveSameAccountRetryLimit(9))
		require.True(t, failoverErr.RequestScopedTransient)
		(&GatewayService{accountRepo: repo}).TempUnscheduleRetryableError(context.Background(), account.ID, failoverErr)
		require.Zero(t, repo.tempUnschedCalls)
	})

	t.Run("disabled preserves legacy cooldown and failover metadata", func(t *testing.T) {
		repo := &grokQuotaAccountRepo{}
		svc := newGrokForbiddenRetryTestService(t, repo, false)

		svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)
		require.Equal(t, 1, repo.tempUnschedCalls)

		failoverErr := svc.newGrokUpstreamFailoverError(
			context.Background(), account, http.StatusForbidden, http.Header{}, body,
			"subscription required", false,
		)
		require.False(t, failoverErr.RetryableOnSameAccount)
		require.Zero(t, failoverErr.SameAccountRetryLimit)
		require.False(t, failoverErr.RequestScopedTransient)
	})

	t.Run("content policy rejection is excluded", func(t *testing.T) {
		repo := &grokQuotaAccountRepo{}
		svc := newGrokForbiddenRetryTestService(t, repo, true)
		contentBody := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)
		require.False(t, svc.shouldRetryGrokOAuthForbidden(context.Background(), account, http.StatusForbidden, contentBody))
	})

	t.Run("API key and non-403 responses are unaffected", func(t *testing.T) {
		repo := &grokQuotaAccountRepo{}
		svc := newGrokForbiddenRetryTestService(t, repo, true)
		apiKeyAccount := &Account{ID: 4811, Platform: PlatformGrok, Type: AccountTypeAPIKey}
		require.False(t, svc.shouldRetryGrokOAuthForbidden(context.Background(), apiKeyAccount, http.StatusForbidden, body))
		require.False(t, svc.shouldRetryGrokOAuthForbidden(context.Background(), account, http.StatusUnauthorized, body))
	})
}

func TestAppendGrokUpstreamErrorPreservesSanitizedDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 4812, Name: "grok-oauth", Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"message":"subscription required","url":"https://x.ai/check?access_token=secret-token"}}`)
	headers := http.Header{"Xai-Request-Id": []string{"xai-request-403"}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	svc.appendGrokUpstreamError(c, account, http.StatusForbidden, headers, body, "failover", "subscription required")

	rawEvents, exists := c.Get(OpsUpstreamErrorsKey)
	require.True(t, exists)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "xai-request-403", events[0].UpstreamRequestID)
	require.Equal(t, http.StatusForbidden, events[0].UpstreamStatusCode)
	require.Contains(t, events[0].UpstreamResponseBody, "subscription required")
	require.Contains(t, events[0].UpstreamResponseBody, "access_token=***")
	require.NotContains(t, events[0].UpstreamResponseBody, "secret-token")
}

func TestGrokMediaForbiddenRetryPolicyReturnsThreeRetryFailoverWithoutCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &grokQuotaAccountRepo{}
	svc := newGrokForbiddenRetryTestService(t, repo, true)
	account := &Account{
		ID: 4813, Name: "grok-oauth", Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{map[string]any{
				"error_code":       float64(http.StatusForbidden),
				"keywords":         []any{"subscription required"},
				"duration_minutes": float64(7),
			}},
		},
	}
	body := `{"error":{"message":"subscription required","code":"entitlement_required"}}`
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "Xai-Request-Id": []string{"xai-forbidden-403"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	result, err := svc.handleGrokMediaErrorResponse(
		context.Background(), resp, c, account, "xai-forbidden-403", "grok-imagine",
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 3, failoverErr.SameAccountRetryLimit)
	require.True(t, failoverErr.RequestScopedTransient)
	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)

	rawEvents, exists := c.Get(OpsUpstreamErrorsKey)
	require.True(t, exists)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "xai-forbidden-403", events[0].UpstreamRequestID)
	require.JSONEq(t, body, events[0].UpstreamResponseBody)
}

func TestHandleGrokAccountUpstreamErrorDefaultCooldownsRespectPoolMode(t *testing.T) {
	for _, statusCode := range []int{
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusInternalServerError,
	} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			repo := &grokQuotaAccountRepo{}
			svc := &OpenAIGatewayService{accountRepo: repo}
			account := &Account{
				ID:       int64(4800 + statusCode),
				Platform: PlatformGrok,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"pool_mode": true,
				},
			}
			body := []byte(`{"error":{"message":"grok access or entitlement denied"}}`)

			svc.handleGrokAccountUpstreamError(
				context.Background(), account, statusCode, nil, body,
			)

			require.Zero(t, repo.tempUnschedCalls)
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
			require.Nil(t, account.TempUnschedulableUntil)
			require.Empty(t, account.TempUnschedulableReason)
			require.True(t, svc.shouldFailoverGrokUpstreamError(statusCode, body))
		})
	}

	account := &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{"pool_mode": true}}
	require.True(t, account.IsPoolModeRetryableStatus(http.StatusForbidden))

	t.Run("explicit temporary rule still applies", func(t *testing.T) {
		repo := &grokQuotaAccountRepo{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		account := &Account{
			ID:       4723,
			Platform: PlatformGrok,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"pool_mode":                  true,
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusForbidden),
						"keywords":         []any{"entitlement denied"},
						"duration_minutes": float64(7),
					},
				},
			},
		}
		before := time.Now()

		svc.handleGrokAccountUpstreamError(
			context.Background(), account, http.StatusForbidden, nil,
			[]byte(`{"error":{"message":"grok access or entitlement denied"}}`),
		)

		require.Equal(t, 1, repo.tempUnschedCalls)
		require.Equal(t, "grok configured forbidden rule", repo.lastTempUnschedReason)
		require.WithinDuration(t, before.Add(7*time.Minute), repo.lastTempUnschedUntil, time.Second)
		require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	})
}

func TestHandleGrokAccountUpstreamError403UsesConfiguredRule(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       4717,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"subscription"},
					"duration_minutes": float64(7),
				},
			},
		},
	}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Greater(t, repo.lastTempUnschedUntil, before.Add(6*time.Minute))
	require.Less(t, repo.lastTempUnschedUntil, before.Add(8*time.Minute))
}

func TestHandleGrokAccountUpstreamError403ConfiguredUnmatchedKeepsDefaultCooldown(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       4718,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"different failure"},
					"duration_minutes": float64(7),
				},
			},
		},
	}

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}
