package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func diagnosticTestService() *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{CodexStateDiagnostics: config.CodexStateDiagnosticsConfig{Enabled: true, AccountIDs: []int64{42, 43}, SamplePercent: 100}}}}
}

func TestCodexDiagnosticsSelectionAndRedaction(t *testing.T) {
	s := diagnosticTestService()
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "private-request-id")
	fields := s.codexDiagnosticFields(ctx, account)
	require.NotEmpty(t, fields)
	enc := zapcore.NewMapObjectEncoder()
	for _, field := range fields {
		field.AddTo(enc)
	}
	require.NotContains(t, enc.Fields, "account_id")
	require.NotContains(t, enc.Fields, "request_id")
	require.NotEqual(t, "private-request-id", enc.Fields["request_ref"])
	require.Equal(t, true, enc.Fields["ops_system_log_skip"])
	require.Equal(t, codexDiagnosticHash("private-request-id"), enc.Fields["request_ref"])
	account.ID = 99
	require.Empty(t, s.codexDiagnosticFields(ctx, account))
	account.ID = 42
	account.Type = AccountTypeAPIKey
	require.Empty(t, s.codexDiagnosticFields(ctx, account))
	account.Type = AccountTypeSetupToken
	require.Empty(t, s.codexDiagnosticFields(ctx, account))
	account.Type = AccountTypeOAuth
	s.cfg.Gateway.CodexStateDiagnostics.Enabled = false
	require.Empty(t, s.codexDiagnosticFields(ctx, account))
	require.Empty(t, codexDiagnosticHash(""))
	require.NotContains(t, diagnosticModel("gpt-secret\nAuthorization: Bearer private"), "private")
	require.Equal(t, "other", diagnosticEffort("Bearer private"))
}

func TestCodexDiagnosticsSamplingStableAcrossAccounts(t *testing.T) {
	s := diagnosticTestService()
	s.cfg.Gateway.CodexStateDiagnostics.SamplePercent = 30
	for _, id := range []string{"first", "second", "third"} {
		ctx := context.WithValue(context.Background(), ctxkey.RequestID, id)
		a := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		selected := len(s.codexDiagnosticFields(ctx, a)) > 0
		a.ID = 43
		require.Equal(t, selected, len(s.codexDiagnosticFields(ctx, a)) > 0)
	}
}

func newDiagnosticTestBody(raw string, sse bool, status int) (*codexDiagnosticBody, *[]map[string]any) {
	events := []map[string]any{}
	b := &codexDiagnosticBody{ReadCloser: io.NopCloser(strings.NewReader(raw)), ctx: context.Background(), start: time.Now(), status: status, sse: sse}
	b.emit = func(event string, fields ...zap.Field) {
		enc := zapcore.NewMapObjectEncoder()
		for _, f := range fields {
			f.AddTo(enc)
		}
		enc.Fields["event"] = event
		events = append(events, enc.Fields)
	}
	return b, &events
}

func TestCodexDiagnosticBodyPreservesSSEAndBoundsMemory(t *testing.T) {
	raw := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"TOP-SECRET\"}\r\n\r\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"usage_limit_reached\",\"message\":\"Bearer secret\"}}}\n\n"
	b, events := newDiagnosticTestBody(raw, true, 292)
	var output bytes.Buffer
	p := make([]byte, 7)
	for {
		n, err := b.Read(p)
		_, _ = output.Write(p[:n])
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}
	require.Equal(t, raw, output.String())
	require.NoError(t, b.Close())
	require.Len(t, *events, 2)
	require.Equal(t, "quota_exhausted", (*events)[1]["error_class"])
	require.EqualValues(t, 292, (*events)[1]["upstream_status"])
	require.NotContains(t, (*events)[1], "message")
	require.Nil(t, b.buf)
	huge := "data: " + strings.Repeat("x", codexDiagnosticLimit*3) + "\n\ndata: {\"type\":\"response.completed\"}\n\n"
	b, events = newDiagnosticTestBody(huge, true, 200)
	data, err := io.ReadAll(b)
	require.NoError(t, err)
	require.Equal(t, huge, string(data))
	require.NoError(t, b.Close())
	require.Equal(t, true, (*events)[0]["observation_truncated"])
	require.Equal(t, "response.completed", (*events)[0]["terminal_event"])
}

func TestCodexDiagnosticBodyJSONCancelAndConcurrentClose(t *testing.T) {
	b, events := newDiagnosticTestBody(`{"error":{"code":"overloaded","message":"secret"}}`, false, 503)
	data, err := io.ReadAll(b)
	require.NoError(t, err)
	require.Contains(t, string(data), "secret")
	require.Equal(t, "overload", (*events)[0]["error_class"])
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = b.Close() }()
	}
	wg.Wait()
	require.Len(t, *events, 1)
	b, events = newDiagnosticTestBody("", true, 200)
	ctx, cancel := context.WithCancel(context.Background())
	b.ctx = ctx
	cancel()
	require.NoError(t, b.Close())
	require.Equal(t, "canceled", (*events)[0]["outcome"])
}

func TestCodexDiagnosticTransportDoesNotConsumeRequestOrResponse(t *testing.T) {
	s := diagnosticTestService()
	a := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	req := httptest.NewRequest(http.MethodPost, "https://upstream.example/responses", nil)
	req.Body = io.NopCloser(strings.NewReader("original"))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(`{"model":"gpt-test","reasoning":{"effort":"xhigh"},"input":"secret"}`)), nil
	}
	finish := s.observeCodexHTTPAttempt(req, "http://name:password@proxy.invalid", a)
	response := &http.Response{StatusCode: 292, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("original-response"))}
	finish(response, nil)
	original, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, "original", string(original))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "original-response", string(body))
	require.Equal(t, 292, response.StatusCode)
	require.NoError(t, response.Body.Close())
	originalBody := io.NopCloser(strings.NewReader("error-response"))
	response.Body = originalBody
	finish(response, errors.New("secret transport error"))
	require.Equal(t, originalBody, response.Body)
	s.cfg.Gateway.CodexStateDiagnostics.Enabled = false
	s.observeCodexHTTPAttempt(req, "", a)(response, nil)
	require.Equal(t, originalBody, response.Body)
}

func TestCodexDiagnosticsDoNotChangeStateGuards(t *testing.T) {
	s := diagnosticTestService()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	a := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	other := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	s.noteOpenAICodexTurnStateOrigin(c, a, "private-state")
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "private-state")
	s.guardOpenAICodexTurnStateEcho(c, a, h)
	require.Equal(t, "private-state", h.Get(openAICodexTurnStateHeader))
	s.guardOpenAICodexTurnStateEcho(c, other, h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, "unknown", s.guardOpenAICodexTurnStateValue(c, other, "unknown"))
	require.Empty(t, s.guardOpenAICodexTurnStateValue(c, other, "private-state"))
	payload := []byte(`{"type":"response.create","client_metadata":{"x-codex-turn-state":"private-state"}}`)
	copyPayload := append([]byte{}, payload...)
	s.observeCodexWSFrame(c, a, payload, true)
	require.Equal(t, copyPayload, payload)
}

func TestCodexDiagnosticMultilineAndExistingCapacityClassifier(t *testing.T) {
	raw := "data: {\"type\":\"response.failed\",\r\ndata: \"response\":{\"error\":{\"code\":\"server_is_overloaded\"}}}\r\n\r\n"
	b, events := newDiagnosticTestBody(raw, true, 200)
	data, err := io.ReadAll(b)
	require.NoError(t, err)
	require.Equal(t, raw, string(data))
	require.Len(t, *events, 1)
	require.Equal(t, "overload", (*events)[0]["error_class"])
	require.Equal(t, "overload", diagnosticErrorClass(200, []byte(`{"type":"error","error":{"message":"Our servers are currently overloaded. Please try again."}}`)))
}

func TestCodexDiagnosticCompressedRequestAndDisabledFastPath(t *testing.T) {
	encoder, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	encoded := encoder.EncodeAll([]byte(`{"model":"gpt-test","reasoning":{"effort":"xhigh"},"input":"private prompt"}`), nil)
	require.NoError(t, encoder.Close())
	req, err := http.NewRequest(http.MethodPost, "https://upstream.invalid/responses", bytes.NewReader(encoded))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "zstd")
	getBody := req.GetBody
	copies := 0
	req.GetBody = func() (io.ReadCloser, error) { copies++; return getBody() }
	s := diagnosticTestService()
	a := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	s.cfg.Gateway.CodexStateDiagnostics.Enabled = false
	s.observeCodexHTTPAttempt(req, "", a)(nil, nil)
	require.Zero(t, copies)
	s.cfg.Gateway.CodexStateDiagnostics.Enabled = true
	s.observeCodexHTTPAttempt(req, "", a)(nil, nil)
	require.Equal(t, 1, copies)
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, encoded, body)
}
