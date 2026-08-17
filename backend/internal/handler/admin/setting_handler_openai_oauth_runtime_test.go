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

func TestSettingHandlerOpenAIOAuthRuntimeGetUsesLegacyFallback(t *testing.T) {
	handler, repo := newOpenAIOAuthRuntimeHandler()
	repo.values[service.SettingKeyOpenAIOAuthNewAccountNoopToolcallDefaultsEnabled] = "true"

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.GetOpenAIOAuthRuntimeSettings, http.MethodGet, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	settings := decodeOpenAIOAuthRuntimeResponse(t, recorder)
	require.True(t, settings.NoopToolcallInjectionEnabled)
	require.True(t, settings.Dynamic429Scheduling.Enabled)
}

func TestSettingHandlerOpenAIOAuthRuntimePatchIsPartial(t *testing.T) {
	handler, repo := newOpenAIOAuthRuntimeHandler()

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.UpdateOpenAIOAuthRuntimeSettings, http.MethodPatch, map[string]any{
		"noop_toolcall_injection_enabled": true,
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	settings := decodeOpenAIOAuthRuntimeResponse(t, recorder)
	require.True(t, settings.NoopToolcallInjectionEnabled)
	require.False(t, settings.Dynamic429Scheduling.Enabled)

	var persisted service.OpenAIOAuthRuntimeSettings
	require.NoError(t, json.Unmarshal([]byte(repo.values[service.SettingKeyOpenAIOAuthRuntimeSettings]), &persisted))
	require.True(t, persisted.NoopToolcallInjectionEnabled)
	require.False(t, persisted.Dynamic429Scheduling.Enabled)
	require.False(t, persisted.SafePreOutputOverloadRetryEnabled)
}

func TestSettingHandlerOpenAIOAuthRuntimePatchSafePreOutputRetryIsPartial(t *testing.T) {
	handler, repo := newOpenAIOAuthRuntimeHandler()

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.UpdateOpenAIOAuthRuntimeSettings, http.MethodPatch, map[string]any{
		"safe_pre_output_overload_retry_enabled": true,
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	settings := decodeOpenAIOAuthRuntimeResponse(t, recorder)
	require.True(t, settings.SafePreOutputOverloadRetryEnabled)
	require.False(t, settings.NoopToolcallInjectionEnabled)
	require.False(t, settings.Dynamic429Scheduling.Enabled)

	var persisted service.OpenAIOAuthRuntimeSettings
	require.NoError(t, json.Unmarshal([]byte(repo.values[service.SettingKeyOpenAIOAuthRuntimeSettings]), &persisted))
	require.True(t, persisted.SafePreOutputOverloadRetryEnabled)
}

func TestSettingHandlerOpenAIOAuthRuntimePatchPlanGatedCooldownIsPartial(t *testing.T) {
	handler, repo := newOpenAIOAuthRuntimeHandler()

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.UpdateOpenAIOAuthRuntimeSettings, http.MethodPatch, map[string]any{
		"plan_gated_model_cooldown_enabled": false,
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	settings := decodeOpenAIOAuthRuntimeResponse(t, recorder)
	require.False(t, settings.PlanGatedModelCooldownEnabled)
	require.False(t, settings.NoopToolcallInjectionEnabled)

	var persisted service.OpenAIOAuthRuntimeSettings
	require.NoError(t, json.Unmarshal([]byte(repo.values[service.SettingKeyOpenAIOAuthRuntimeSettings]), &persisted))
	require.False(t, persisted.PlanGatedModelCooldownEnabled)
}

func TestSettingHandlerOpenAIOAuthRuntimePatchRateLimitSameAccountRetryIsPartial(t *testing.T) {
	handler, repo := newOpenAIOAuthRuntimeHandler()

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.UpdateOpenAIOAuthRuntimeSettings, http.MethodPatch, map[string]any{
		"openai_oauth_rate_limit_same_account_retry_enabled": true,
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	settings := decodeOpenAIOAuthRuntimeResponse(t, recorder)
	require.True(t, settings.OpenAIRateLimitSameAccountRetryEnabled)
	require.False(t, settings.NoopToolcallInjectionEnabled)

	var persisted service.OpenAIOAuthRuntimeSettings
	require.NoError(t, json.Unmarshal([]byte(repo.values[service.SettingKeyOpenAIOAuthRuntimeSettings]), &persisted))
	require.True(t, persisted.OpenAIRateLimitSameAccountRetryEnabled)
}

func TestSettingHandlerOpenAIOAuthRuntimePatchDynamicIgnoresClientRevision(t *testing.T) {
	handler, _ := newOpenAIOAuthRuntimeHandler()
	dynamic := service.DefaultOpenAIOAuthRuntimeSettings(false).Dynamic429Scheduling
	dynamic.Enabled = true
	dynamic.MinimumSamples = 40
	dynamic.Revision = 88

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.UpdateOpenAIOAuthRuntimeSettings, http.MethodPatch, map[string]any{
		"dynamic_429_scheduling": dynamic,
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	settings := decodeOpenAIOAuthRuntimeResponse(t, recorder)
	require.Equal(t, 40, settings.Dynamic429Scheduling.MinimumSamples)
	require.Equal(t, int64(2), settings.Dynamic429Scheduling.Revision)
}

func TestSettingHandlerOpenAIOAuthRuntimePatchRejectsInvalidDynamicSettings(t *testing.T) {
	handler, _ := newOpenAIOAuthRuntimeHandler()
	dynamic := service.DefaultOpenAIOAuthRuntimeSettings(true).Dynamic429Scheduling
	dynamic.Minimum429Count = dynamic.MinimumSamples + 1

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.UpdateOpenAIOAuthRuntimeSettings, http.MethodPatch, map[string]any{
		"dynamic_429_scheduling": dynamic,
	})
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "minimum_429_count")
}

func TestSettingHandlerOpenAIOAuthRuntimePatchRejectsEmptyPayload(t *testing.T) {
	handler, _ := newOpenAIOAuthRuntimeHandler()

	recorder := performOpenAIOAuthRuntimeRequest(t, handler.UpdateOpenAIOAuthRuntimeSettings, http.MethodPatch, map[string]any{})
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
