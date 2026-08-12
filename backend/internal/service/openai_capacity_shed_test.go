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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// --- mock: 只记录临时不可调度写入，其余方法不应被调用 ---

type capacityShedAccountRepoStub struct {
	AccountRepository // 嵌入接口，未实现的方法会 panic（不应被调用）

	tempUnschedCalls int
}

func (r *capacityShedAccountRepoStub) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.tempUnschedCalls++
	return nil
}

// 上游容量降载是请求级信号：故障因素（客户端身份、模型容量）与账号无关，
// 同账号重试用尽后不得把账号临时摘掉——否则一个被降载的请求会顺着 failover
// 把整池账号逐个封禁，而每个账号都会以同一个错误失败。
func TestTempUnscheduleRetryableErrorSkipsRequestScopedTransient(t *testing.T) {
	t.Run("请求级瞬时故障不写账号状态", func(t *testing.T) {
		repo := &capacityShedAccountRepoStub{}
		svc := &GatewayService{accountRepo: repo}

		svc.TempUnscheduleRetryableError(context.Background(), 1, &UpstreamFailoverError{
			StatusCode:             http.StatusBadGateway,
			RetryableOnSameAccount: true,
			RequestScopedTransient: true,
		})

		require.Zero(t, repo.tempUnschedCalls)
	})

	// 对照组：同样的 502 在未标记请求级瞬时故障时仍按原有语义临时摘号，
	// 确认上面的断言来自新增守卫而非其他前置条件。
	t.Run("未标记时保持原有临时摘号语义", func(t *testing.T) {
		repo := &capacityShedAccountRepoStub{}
		svc := &GatewayService{accountRepo: repo}

		svc.TempUnscheduleRetryableError(context.Background(), 1, &UpstreamFailoverError{
			StatusCode:             http.StatusBadGateway,
			RetryableOnSameAccount: true,
		})

		require.Equal(t, 1, repo.tempUnschedCalls)
	})
}

// 非池模式账号同样要先在同账号重试：换号不改变降载因素。
func TestStreamFailedEventCapacityShedRetriesOnSameAccount(t *testing.T) {
	nonPool := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	for _, code := range []string{"server_is_overloaded", "slow_down"} {
		payload := []byte(`{"type":"response.failed","response":{"error":{"code":"` + code + `"}}}`)
		require.True(t, isOpenAIUpstreamCapacityShedEvent(payload), code)
		require.True(t, openAIStreamFailedEventRetryableOnSameAccount(nonPool, payload, "overloaded"), code)
	}

	messageOnly := []byte(`{"type":"response.failed","response":{"error":{"message":"Our servers are currently overloaded. Please try again later."}}}`)
	require.True(t, isOpenAIUpstreamCapacityShedSignal(messageOnly, "Our servers are currently overloaded. Please try again later."))
	require.True(t, openAIStreamFailedEventRetryableOnSameAccount(nonPool, messageOnly, "Our servers are currently overloaded. Please try again later."))
	require.False(t, isOpenAIUpstreamCapacityShedSignal(messageOnly, "An error occurred while processing your request."))

	// 非降载的 failed 事件在非池模式下仍不做同账号重试，避免放大改动面。
	other := []byte(`{"type":"response.failed","response":{"error":{"code":"server_error"}}}`)
	require.False(t, isOpenAIUpstreamCapacityShedEvent(other))
	require.False(t, openAIStreamFailedEventRetryableOnSameAccount(nonPool, other, "boom"))
}

func TestOpenAIMessageOnlyCapacityShedBuildsRequestScopedFailover(t *testing.T) {
	message := "Our servers are currently overloaded. Please try again later."
	payload := []byte(`{"type":"response.failed","response":{"error":{"message":"` + message + `"}}}`)
	svc := &OpenAIGatewayService{}

	failoverErr := svc.newOpenAIStreamFailoverError(
		nil,
		&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		false,
		"",
		payload,
		message,
	)

	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.True(t, failoverErr.RequestScopedTransient)
	require.Equal(t, 10, failoverErr.SameAccountRetryLimit)
	require.Equal(t, 200*time.Millisecond, failoverErr.SameAccountRetryDelay)
}

func TestOpenAIOrdinaryStreamFailureKeepsRetryFallbackMetadata(t *testing.T) {
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"temporary"}}}`)
	svc := &OpenAIGatewayService{}

	failoverErr := svc.newOpenAIStreamFailoverError(
		nil,
		&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		false,
		"",
		payload,
		"temporary",
	)

	require.Zero(t, failoverErr.SameAccountRetryLimit)
	require.Zero(t, failoverErr.SameAccountRetryDelay)
	require.Equal(t, 500*time.Millisecond, failoverErr.EffectiveSameAccountRetryDelay(500*time.Millisecond))
}

// 上游降载的真实序列是「event: error → event: response.failed」。error 帧不算
// 客户端输出：若把它当首输出 flush，clientOutputStarted 被固化，随后的 failed
// 事件就进不了 pre-output failover 分支，只能把致命错误原样转发给客户端。
func TestOpenAIStreamErrorFrameDoesNotStartClientOutput(t *testing.T) {
	cases := []struct {
		data      string
		eventType string
		want      bool
	}{
		{`{"type":"error","error":{"code":"server_is_overloaded","message":"overloaded"}}`, "error", false},
		{`{"type":"error","error":{"code":"slow_down","message":"slow down"}}`, "error", false},
		{`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"limited"}}`, "error", false},
		// 不可重试类错误帧维持原样转发（不进 failover），保留上游错误细节。
		{`{"type":"error","error":{"type":"invalid_request_error","code":"content_policy_violation","message":"blocked"}}`, "error", true},
		{`{"type":"response.failed","response":{"error":{"code":"server_is_overloaded"}}}`, "response.failed", false},
		{`{"type":"response.created","response":{"id":"resp_1"}}`, "response.created", false},
		{`{"type":"response.in_progress","response":{"id":"resp_1"}}`, "response.in_progress", false},
		{`{"type":"response.output_text.delta","delta":"hi"}`, "response.output_text.delta", true},
		{`[DONE]`, "", true},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, openAIStreamDataStartsClientOutput(tc.data, tc.eventType), "data=%s type=%s", tc.data, tc.eventType)
	}
}

func TestOpenAIStreamSafePreOutputMetadataBuffer(t *testing.T) {
	metadata := []struct {
		data     string
		typeName string
	}{
		{`{"type":"response.output_item.added","item":{"type":"message","status":"in_progress"}}`, "response.output_item.added"},
		{`{"type":"response.content_part.added","part":{"type":"output_text"}}`, "response.content_part.added"},
		{`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_1"}}`, "response.output_item.done"},
	}
	for _, tc := range metadata {
		require.True(t, openAIStreamEventIsSafePreOutputMetadata(tc.data, tc.typeName), tc.typeName)
		require.False(t, openAIStreamDataStartsClientOutputWithSafePreOutputRetry(tc.data, tc.typeName, true), tc.typeName)
	}

	semantic := `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"hello"}]}}`
	require.False(t, openAIStreamEventIsSafePreOutputMetadata(semantic, "response.output_item.done"))
	require.True(t, openAIStreamDataStartsClientOutputWithSafePreOutputRetry(semantic, "response.output_item.done", true))
	require.True(t, openAIStreamDataStartsClientOutputWithSafePreOutputRetry(metadata[0].data, metadata[0].typeName, false))
}

func TestOpenAIOverloadStreamTrackerStages(t *testing.T) {
	tracker := newOpenAIOverloadStreamTracker()
	tracker.Observe([]byte(`{"type":"response.created","response":{"id":"resp_stage"}}`), "response.created")
	got := tracker.Snapshot(false)
	require.Equal(t, "structural_only", got.StreamStage)
	require.False(t, got.SemanticOutputStarted)
	require.Equal(t, "response.created", got.LastEventType)

	tracker.Observe([]byte(`{"type":"response.output_item.added","item":{"type":"message"}}`), "response.output_item.added")
	got = tracker.Snapshot(false)
	require.Equal(t, "output_started", got.StreamStage)
	require.False(t, got.SemanticOutputStarted)

	tracker.Observe([]byte(`{"type":"response.output_text.delta","delta":"hello"}`), "response.output_text.delta")
	got = tracker.Snapshot(true)
	require.Equal(t, "semantic_output", got.StreamStage)
	require.True(t, got.SemanticOutputStarted)
	require.Equal(t, "text", got.SemanticOutputKind)
	require.True(t, got.DownstreamCommitted)
	require.Equal(t, "resp_stage", got.UpstreamResponseID)
}

func TestOpenAIOverloadDiagnosticsFromError(t *testing.T) {
	diagnostics := OpenAIOverloadStreamDiagnostics{
		LastEventType:         "response.output_text.delta",
		EventCount:            4,
		StreamStage:           "semantic_output",
		SemanticOutputStarted: true,
		SemanticOutputKind:    "text",
		DownstreamCommitted:   true,
	}
	exposed := &OpenAIOverloadExposedError{Message: "Our servers are currently overloaded. Please try again later.", Diagnostics: diagnostics}
	got, ok := OpenAIOverloadDiagnosticsFromError(exposed)
	require.True(t, ok)
	require.Equal(t, diagnostics, *got)

	failover := &UpstreamFailoverError{RequestScopedTransient: true, OverloadDiagnostics: &diagnostics}
	got, ok = OpenAIOverloadDiagnosticsFromError(failover)
	require.True(t, ok)
	require.Equal(t, diagnostics, *got)

	got, ok = OpenAIOverloadDiagnosticsFromError(errors.New("unrelated"))
	require.False(t, ok)
	require.Nil(t, got)
}

func TestOpenAIStreamSafePreOutputMetadataStillFailsOverBeforeClientOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	settingsRepo := newOpenAIOAuthRuntimeSettingRepo()
	settings := DefaultOpenAIOAuthRuntimeSettings(false)
	settings.SafePreOutputOverloadRetryEnabled = true
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	settingsRepo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(settingsJSON)
	settingService := NewSettingService(settingsRepo, nil)
	require.True(t, settingService.GetOpenAIOAuthRuntimeSettings(context.Background()).SafePreOutputOverloadRetryEnabled)
	svc := &OpenAIGatewayService{cfg: cfg, settingService: settingService}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.output_item.added",
			`data: {"type":"response.output_item.added","item":{"type":"message","status":"in_progress"}}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-safe-pre-output"}},
	}

	_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "acc"}, time.Now(), "model", "model")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RequestScopedTransient)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStandardStreamSafePreOutputMetadataStillFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settingsRepo := newOpenAIOAuthRuntimeSettingRepo()
	settings := DefaultOpenAIOAuthRuntimeSettings(false)
	settings.SafePreOutputOverloadRetryEnabled = true
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	settingsRepo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(settingsJSON)
	svc := &OpenAIGatewayService{
		cfg:            &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		settingService: NewSettingService(settingsRepo, nil),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_standard"}}`,
			"",
			"event: response.output_item.added",
			`data: {"type":"response.output_item.added","item":{"type":"message","status":"in_progress"}}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_standard","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-safe-pre-output-standard"}},
	}

	_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "standard"}, time.Now(), "model", "model")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RequestScopedTransient)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStandardStreamSafePreOutputLargePreambleDoesNotCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settingsRepo := newOpenAIOAuthRuntimeSettingRepo()
	settings := DefaultOpenAIOAuthRuntimeSettings(false)
	settings.SafePreOutputOverloadRetryEnabled = true
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	settingsRepo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(settingsJSON)

	largeInstructions := strings.Repeat("large instruction ", 900)
	created, err := json.Marshal(map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":           "resp_large_preamble",
			"instructions": largeInstructions,
		},
	})
	require.NoError(t, err)
	inProgress, err := json.Marshal(map[string]any{
		"type": "response.in_progress",
		"response": map[string]any{
			"id":           "resp_large_preamble",
			"instructions": largeInstructions,
		},
	})
	require.NoError(t, err)

	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &OpenAIGatewayService{
		cfg:            cfg,
		settingService: NewSettingService(settingsRepo, nil),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			"data: " + string(created),
			"",
			"event: response.in_progress",
			"data: " + string(inProgress),
			"",
			"event: error",
			`data: {"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_large_preamble","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-large-preamble"}},
	}

	_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "large-preamble",
	}, time.Now(), "model", "model")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RequestScopedTransient)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStandardStreamSafePreOutputLargePreambleCommitsOnSemanticOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settingsRepo := newOpenAIOAuthRuntimeSettingRepo()
	settings := DefaultOpenAIOAuthRuntimeSettings(false)
	settings.SafePreOutputOverloadRetryEnabled = true
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	settingsRepo.values[SettingKeyOpenAIOAuthRuntimeSettings] = string(settingsJSON)

	largeInstructions := strings.Repeat("large instruction ", 900)
	created, err := json.Marshal(map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":           "resp_large_success",
			"instructions": largeInstructions,
		},
	})
	require.NoError(t, err)
	inProgress, err := json.Marshal(map[string]any{
		"type": "response.in_progress",
		"response": map[string]any{
			"id":           "resp_large_success",
			"instructions": largeInstructions,
		},
	})
	require.NoError(t, err)

	svc := &OpenAIGatewayService{
		cfg:            &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		settingService: NewSettingService(settingsRepo, nil),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			"data: " + string(created),
			"",
			"event: response.in_progress",
			"data: " + string(inProgress),
			"",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			"",
			"event: response.completed",
			`data: {"type":"response.completed","response":{"id":"resp_large_success","status":"completed"}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-large-success"}},
	}

	_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "large-success",
	}, time.Now(), "model", "model")
	require.NoError(t, err)
	require.True(t, c.Writer.Written())
	require.Contains(t, rec.Body.String(), "large instruction ")
	require.Contains(t, rec.Body.String(), `"delta":"hello"`)
}

// 回归用例（真实上游降载序列）：created → in_progress → error 帧 → response.failed。
// 期望仍然走 pre-output failover（同账号重试 + 请求级瞬时标记），且不向客户端写出任何字节。
func TestOpenAIStreamCapacityShedErrorFramePrecedingFailedStillFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"},"sequence_number":0}`,
			"",
			"event: response.in_progress",
			`data: {"type":"response.in_progress","response":{"id":"resp_1"},"sequence_number":1}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."},"sequence_number":2}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}},"sequence_number":3}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-shed-error-then-failed"}},
	}

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "acc"}, time.Now(), "model", "model")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.True(t, failoverErr.RequestScopedTransient)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

// 流中途（已有真实输出）降载时无法再 failover，此时必须把降载码改写为客户端
// 可重试的 server_error 再转发——Codex 对 server_is_overloaded/slow_down 判致命
// 并终止会话，对其余错误码执行内置退避重试。消息原样保留。
func TestOpenAIStreamCapacityShedAfterOutputRewritesCodeForClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","delta":"partial"}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."},"sequence_number":2}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}},"sequence_number":3}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-shed-after-output"}},
	}

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "acc"}, time.Now(), "model", "model")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))

	body := rec.Body.String()
	require.Contains(t, body, "partial")
	require.Contains(t, body, "event: response.failed")
	require.Contains(t, body, `"code":"server_error"`)
	require.NotContains(t, body, "server_is_overloaded")
	require.Contains(t, body, "Our servers are currently overloaded")
}

// helper 单测：只有降载码被改写，其余错误码（尤其 rate_limit_exceeded，客户端
// 依赖其原码解析重试延时）必须原样保留。
func TestSanitizeOpenAICapacityShedErrorCodeForClient(t *testing.T) {
	cases := []struct {
		name        string
		payload     string
		wantChanged bool
		wantContain string
	}{
		{
			name:        "failed事件嵌套code改写",
			payload:     `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"overloaded"}}}`,
			wantChanged: true,
			wantContain: `"code":"server_error"`,
		},
		{
			name:        "error帧裸code改写",
			payload:     `{"type":"error","error":{"code":"slow_down","message":"slow down"}}`,
			wantChanged: true,
			wantContain: `"code":"server_error"`,
		},
		{
			name:        "rate_limit不改写",
			payload:     `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"try again in 3s"}}}`,
			wantChanged: false,
			wantContain: `"code":"rate_limit_exceeded"`,
		},
		{
			name:        "普通server_error不改写",
			payload:     `{"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`,
			wantChanged: false,
			wantContain: `"code":"server_error"`,
		},
		{
			name:        "非JSON不改写",
			payload:     `not-json`,
			wantChanged: false,
			wantContain: `not-json`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := sanitizeOpenAICapacityShedErrorCodeForClient([]byte(tc.payload))
			require.Equal(t, tc.wantChanged, changed)
			require.Contains(t, string(out), tc.wantContain)
			if changed {
				require.NotContains(t, string(out), "server_is_overloaded")
				require.NotContains(t, string(out), "slow_down")
			}
		})
	}
}

// 出站身份的版本声明只能有一个来源：UA 的版本段、version 头、探针版本三处必须同源，
// 各自硬编码会漂移成互相矛盾的身份，而自相矛盾或陈旧的身份会被上游优先降载。
func TestCodexOutboundVersionHasSingleSource(t *testing.T) {
	require.True(t,
		strings.HasPrefix(codexCLIUserAgent, openai.CodexDefaultOriginator+"/"+codexCLIVersion+" "),
		"codexCLIUserAgent=%q 必须以 codexCLIVersion=%q 作为版本段", codexCLIUserAgent, codexCLIVersion,
	)
	require.Equal(t, codexCLIVersion, openAICodexProbeVersion)
	require.GreaterOrEqual(t, CompareVersions(codexCLIVersion, codexUpstreamMinVersion), 0,
		"codexCLIVersion=%q 不得低于上游最低门槛 %q", codexCLIVersion, codexUpstreamMinVersion,
	)
}
