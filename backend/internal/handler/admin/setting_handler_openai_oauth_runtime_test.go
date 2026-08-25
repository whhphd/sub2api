package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIOAuthRuntimeHandlerRepo struct {
	values map[string]string
}

func newOpenAIOAuthRuntimeHandlerRepo() *openAIOAuthRuntimeHandlerRepo {
	return &openAIOAuthRuntimeHandlerRepo{values: make(map[string]string)}
}

func (r *openAIOAuthRuntimeHandlerRepo) Get(_ context.Context, key string) (*service.Setting, error) {
	value, err := r.GetValue(context.Background(), key)
	if err != nil {
		return nil, err
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (r *openAIOAuthRuntimeHandlerRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *openAIOAuthRuntimeHandlerRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *openAIOAuthRuntimeHandlerRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *openAIOAuthRuntimeHandlerRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	for key, value := range settings {
		if err := r.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *openAIOAuthRuntimeHandlerRepo) GetAll(_ context.Context) (map[string]string, error) {
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *openAIOAuthRuntimeHandlerRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func newOpenAIOAuthRuntimeHandler() (*SettingHandler, *openAIOAuthRuntimeHandlerRepo) {
	gin.SetMode(gin.TestMode)
	repo := newOpenAIOAuthRuntimeHandlerRepo()
	return NewSettingHandler(service.NewSettingService(repo, nil), nil, nil, nil, nil, nil, nil), repo
}

func performOpenAIOAuthRuntimeRequest(t *testing.T, handler gin.HandlerFunc, method string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&requestBody).Encode(body))
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, "/api/v1/admin/settings/openai-oauth-runtime", &requestBody)
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler(ctx)
	return recorder
}

func decodeOpenAIOAuthRuntimeResponse(t *testing.T, recorder *httptest.ResponseRecorder) service.OpenAIOAuthRuntimeSettings {
	t.Helper()
	var envelope struct {
		Data service.OpenAIOAuthRuntimeSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope.Data
}

func TestSettingHandlerOpenAIOAuthRuntimeGetUsesDefaults(t *testing.T) {
	handler, _ := newOpenAIOAuthRuntimeHandler()

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.GetOpenAIOAuthRuntimeSettings, http.MethodGet, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, *service.DefaultOpenAIOAuthRuntimeSettings(false), decodeOpenAIOAuthRuntimeResponse(t, recorder))
}

func TestSettingHandlerOpenAIOAuthRuntimePatchIsPartial(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		assert  func(*testing.T, service.OpenAIOAuthRuntimeSettings)
	}{
		{
			name:    "safe pre-output retry",
			payload: map[string]any{"safe_pre_output_overload_retry_enabled": true},
			assert: func(t *testing.T, settings service.OpenAIOAuthRuntimeSettings) {
				require.True(t, settings.SafePreOutputOverloadRetryEnabled)
				require.True(t, settings.PlanGatedModelCooldownEnabled)
			},
		},
		{
			name:    "plan-gated cooldown",
			payload: map[string]any{"plan_gated_model_cooldown_enabled": false},
			assert: func(t *testing.T, settings service.OpenAIOAuthRuntimeSettings) {
				require.False(t, settings.PlanGatedModelCooldownEnabled)
				require.False(t, settings.SafePreOutputOverloadRetryEnabled)
			},
		},
		{
			name:    "OpenAI rate-limit retry",
			payload: map[string]any{"openai_oauth_rate_limit_same_account_retry_enabled": true},
			assert: func(t *testing.T, settings service.OpenAIOAuthRuntimeSettings) {
				require.True(t, settings.OpenAIRateLimitSameAccountRetryEnabled)
				require.False(t, settings.GrokOAuthForbiddenSameAccountRetryEnabled)
			},
		},
		{
			name:    "Grok forbidden retry",
			payload: map[string]any{"grok_oauth_forbidden_same_account_retry_enabled": true},
			assert: func(t *testing.T, settings service.OpenAIOAuthRuntimeSettings) {
				require.True(t, settings.GrokOAuthForbiddenSameAccountRetryEnabled)
				require.False(t, settings.OpenAIRateLimitSameAccountRetryEnabled)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, repo := newOpenAIOAuthRuntimeHandler()
			recorder := performOpenAIOAuthRuntimeRequest(t, handler.UpdateOpenAIOAuthRuntimeSettings, http.MethodPatch, tt.payload)
			require.Equal(t, http.StatusOK, recorder.Code)
			settings := decodeOpenAIOAuthRuntimeResponse(t, recorder)
			tt.assert(t, settings)

			var persisted service.OpenAIOAuthRuntimeSettings
			require.NoError(t, json.Unmarshal([]byte(repo.values[service.SettingKeyOpenAIOAuthRuntimeSettings]), &persisted))
			require.Equal(t, settings, persisted)
		})
	}
}

func TestSettingHandlerOpenAIOAuthRuntimePatchRejectsEmptyPayload(t *testing.T) {
	handler, _ := newOpenAIOAuthRuntimeHandler()

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.UpdateOpenAIOAuthRuntimeSettings, http.MethodPatch, map[string]any{})
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
