package service

// Passive diagnostics: never inject state, alter routing, or consume a body ahead
// of the caller. Only selected accounts are observed; no payload is logged.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

const codexDiagnosticLimit = 64 << 10

var codexDiagnosticSecret = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	return key
}()
var codexDiagnosticLogBudget = rate.NewLimiter(10, 20)
var codexDiagnosticDropped atomic.Int64
var codexDiagnosticAttempt atomic.Uint64

// Process-local HMAC prevents enumerating low-entropy account/session identifiers.
// Hashes intentionally cannot be joined across process restarts.
func codexDiagnosticHash(value string) string {
	if value == "" || len(codexDiagnosticSecret) == 0 {
		return ""
	}
	h := hmac.New(sha256.New, codexDiagnosticSecret)
	_, _ = h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func (s *OpenAIGatewayService) codexDiagnosticFields(ctx context.Context, account *Account) []zap.Field {
	if s == nil || s.cfg == nil || account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || len(codexDiagnosticSecret) == 0 {
		return nil
	}
	cfg := s.cfg.Gateway.CodexStateDiagnostics
	if !cfg.Enabled || cfg.SamplePercent <= 0 {
		return nil
	}
	selected := cfg.AllAccounts
	for _, id := range cfg.AccountIDs {
		if id == account.ID {
			selected = true
			break
		}
	}
	if !selected {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestID, _ := ctx.Value(ctxkey.RequestID).(string)
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	// Keep all observations/retries of a sampled request together. Without a
	// server request ID sample by account rather than making independent decisions.
	sampleKey := requestID
	if sampleKey == "" {
		sampleKey = "account:" + strconv.FormatInt(account.ID, 10)
	}
	digest := codexDiagnosticHash(sampleKey)
	bucket, _ := strconv.ParseUint(digest[:8], 16, 32)
	if int(bucket%100) >= cfg.SamplePercent {
		return nil
	}
	proxy := "direct"
	if account.ProxyID != nil {
		proxy = strconv.FormatInt(*account.ProxyID, 10)
	}
	return []zap.Field{
		zap.String("component", "service.codex_state_diagnostics"),
		zap.Bool(logger.OpsSystemLogSkipField, true),
		zap.Bool("all_accounts", cfg.AllAccounts),
		zap.Bool("full_capture", cfg.FullCapture),
		zap.Int("sample_percent", cfg.SamplePercent),
		zap.String("request_ref", codexDiagnosticHash(requestID)),
		zap.String("user_ref", codexDiagnosticHash("user:"+strconv.FormatInt(userID, 10))),
		zap.String("account_ref", codexDiagnosticHash("account:"+strconv.FormatInt(account.ID, 10))),
		zap.String("proxy_ref", codexDiagnosticHash("proxy:"+proxy)),
	}
}

func codexDiagnosticEventAllowed(fields []zap.Field, budget *rate.Limiter) bool {
	for _, field := range fields {
		if field.Key == "full_capture" && field.Integer == 1 {
			return true
		}
	}
	return budget.Allow()
}

func emitCodexDiagnostic(fields []zap.Field, event string, extra ...zap.Field) {
	if len(fields) == 0 {
		return
	}
	if !codexDiagnosticEventAllowed(fields, codexDiagnosticLogBudget) {
		codexDiagnosticDropped.Add(1)
		return
	}
	out := append(append([]zap.Field{}, fields...), zap.String("event", event))
	out = append(out, extra...)
	out = append(out, zap.Int64("suppressed_events", codexDiagnosticDropped.Swap(0)))
	// Do not inherit request loggers: they may contain raw user/session identifiers.
	logger.L().Info("codex_state_diagnostic", out...)
}

func diagnosticStateFields(prefix, state string) []zap.Field {
	value := strings.TrimSpace(state)
	// 292/312 are opaque token lengths, NOT HTTP statuses or quality labels.
	// Base64url tokens are ASCII, so their byte and character lengths agree.
	return []zap.Field{
		zap.Bool(prefix+"_present", value != ""),
		zap.Int(prefix+"_length", len(value)),
		zap.String(prefix+"_ref", codexDiagnosticHash(value)),
	}
}

func (s *OpenAIGatewayService) observeCodexState(c *gin.Context, account *Account, event, before, after string) {
	if c == nil || c.Request == nil {
		return
	}
	fields := s.codexDiagnosticFields(c.Request.Context(), account)
	if len(fields) == 0 {
		return
	}
	fields = append(fields, zap.String("session_ref", codexDiagnosticHash(extractClientSessionID(c.Request.Header))), zap.String("api_key_ref", codexDiagnosticHash("api-key:"+strconv.FormatInt(getAPIKeyIDFromContext(c), 10))))
	extra := append(diagnosticStateFields("before", before), diagnosticStateFields("after", after)...)
	emitCodexDiagnostic(fields, event, extra...)
}

// Model is operational metadata, but arbitrary client strings must not become logs.
func diagnosticModel(value string) string {
	if len(value) > 80 || !strings.HasPrefix(value, "gpt-") {
		return "ref:" + codexDiagnosticHash(value)
	}
	for _, r := range value {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._", r)
		if !allowed {
			return "ref:" + codexDiagnosticHash(value)
		}
	}
	return value
}

func diagnosticEffort(value string) string {
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return value
	case "":
		return "unspecified"
	default:
		return "other"
	}
}

func diagnosticErrorClass(status int, payload []byte) string {
	// Reuse the existing narrow capacity classifier, but never log its input.
	if isOpenAIUpstreamCapacityShedSignal(payload, "") {
		return "overload"
	}
	// Never infer a model downgrade from an HTTP status.
	code := gjson.GetBytes(payload, "error.code").String()
	if code == "" {
		code = gjson.GetBytes(payload, "response.error.code").String()
	}
	switch code {
	case "usage_limit_reached", "insufficient_quota", "quota_exceeded":
		return "quota_exhausted"
	case "overloaded", "overloaded_error", "server_overloaded":
		return "overload"
	case "rate_limit_exceeded", "rate_limit_error":
		return "rate_limited"
	}
	if status == 429 {
		return "rate_limited_unclassified"
	}
	if status == 401 || status == 403 {
		return "authentication_or_access"
	}
	if status >= 500 {
		return "upstream_server_error"
	}
	if status >= 400 {
		return "upstream_client_error"
	}
	if code != "" {
		return "other_upstream_error"
	}
	return "none"
}

func (s *OpenAIGatewayService) observeCodexHTTPAttempt(req *http.Request, proxyURL string, account *Account) func(*http.Response, error) {
	noop := func(*http.Response, error) {}
	if req == nil {
		return noop
	}
	fields := s.codexDiagnosticFields(req.Context(), account)
	if len(fields) == 0 {
		return noop
	}
	attempt := codexDiagnosticAttempt.Add(1)
	endpoint := "other"
	if req.URL != nil {
		for _, suffix := range []string{"/responses/compact", "/responses", "/chat/completions", "/models"} {
			if strings.HasSuffix(req.URL.Path, suffix) {
				endpoint = suffix
				break
			}
		}
	}
	fields = append(fields, zap.Uint64("attempt", attempt), zap.String("transport", "http"), zap.String("endpoint", endpoint), zap.String("egress_config_ref", codexDiagnosticHash(proxyURL)), zap.String("session_ref", codexDiagnosticHash(extractClientSessionID(req.Header))))
	fields = append(fields, diagnosticStateFields("sent_state", req.Header.Get(openAICodexTurnStateHeader))...)
	// GetBody opens an independent copy. zstd request decoding is bounded as well.
	if req.GetBody != nil {
		if copyBody, err := req.GetBody(); err == nil {
			var reader io.Reader = copyBody
			var decoder *zstd.Decoder
			encoding := req.Header.Get("Content-Encoding")
			if encoding == "zstd" {
				decoder, err = zstd.NewReader(copyBody, zstd.WithDecoderMaxMemory(8<<20), zstd.WithDecoderConcurrency(1))
				if err == nil {
					reader = decoder
				}
			}
			if err == nil && (encoding == "" || encoding == "zstd") {
				body, _ := io.ReadAll(io.LimitReader(reader, codexDiagnosticLimit))
				fields = append(fields, zap.String("model", diagnosticModel(gjson.GetBytes(body, "model").String())), zap.String("reasoning_effort", diagnosticEffort(gjson.GetBytes(body, "reasoning.effort").String())))
			}
			if decoder != nil {
				decoder.Close()
			}
			_ = copyBody.Close()
		}
	}
	start := time.Now()
	emitCodexDiagnostic(fields, "http_send")
	return func(resp *http.Response, err error) {
		if err != nil {
			outcome := "transport_error"
			if errors.Is(err, context.Canceled) {
				outcome = "canceled"
			} else if errors.Is(err, context.DeadlineExceeded) {
				outcome = "timeout"
			}
			emitCodexDiagnostic(fields, "http_transport_end", zap.String("outcome", outcome), zap.Int64("elapsed_ms", time.Since(start).Milliseconds()))
			return
		}
		if resp == nil {
			return
		}
		extra := append(diagnosticStateFields("received_state", resp.Header.Get(openAICodexTurnStateHeader)), diagnosticStateFields("alternate_state", resp.Header.Get("current_turn_state"))...)
		extra = append(extra, zap.Int("upstream_status", resp.StatusCode), zap.Int64("headers_ms", time.Since(start).Milliseconds()), zap.String("upstream_request_ref", codexDiagnosticHash(resp.Header.Get("x-request-id"))))
		emitCodexDiagnostic(fields, "http_headers", extra...)
		if resp.Body != nil {
			resp.Body = &codexDiagnosticBody{ReadCloser: resp.Body, ctx: req.Context(), start: start, status: resp.StatusCode, sse: strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"), emit: func(event string, extra ...zap.Field) { emitCodexDiagnostic(fields, event, extra...) }}
		}
	}
}

// Reader observation is bounded and fail-open. It returns exactly the bytes and
// errors produced by the original body. Close and Read may run concurrently.
type codexDiagnosticBody struct {
	inspectError func([]byte)
	io.ReadCloser
	mu             sync.Mutex
	ctx            context.Context
	start          time.Time
	status         int
	sse            bool
	buf            []byte
	eventData      []byte
	eventOversized bool
	oversized      bool
	truncated      bool
	firstOutput    bool
	terminal       string
	errorClass     string
	done           bool
	emit           func(string, ...zap.Field)
}

func (b *codexDiagnosticBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return n, err
	}
	if b.sse {
		b.consume(p[:n])
	} else if len(b.buf) < codexDiagnosticLimit {
		count := min(n, codexDiagnosticLimit-len(b.buf))
		b.buf = append(b.buf, p[:count]...)
		b.truncated = b.truncated || count < n
	} else if n > 0 {
		b.truncated = true
	}
	if err != nil {
		b.finishLocked(err)
	}
	return n, err
}

func (b *codexDiagnosticBody) consume(data []byte) {
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		part := data
		if end >= 0 {
			part = data[:end]
		}
		if !b.oversized {
			if len(b.buf)+len(part) > codexDiagnosticLimit {
				b.buf = nil
				b.oversized = true
				b.truncated = true
			} else {
				b.buf = append(b.buf, part...)
			}
		}
		if end < 0 {
			return
		}
		b.consumeLine()

		b.buf = b.buf[:0]
		b.oversized = false
		data = data[end+1:]
	}
}

func (b *codexDiagnosticBody) consumeLine() {
	if b.oversized {
		b.eventOversized = true
		b.eventData = nil
		return
	}
	line := bytes.TrimSuffix(b.buf, []byte("\r"))
	if len(line) == 0 {
		if !b.eventOversized {
			b.observeJSON(b.eventData)
		}
		b.eventData = b.eventData[:0]
		b.eventOversized = false
		return
	}
	if b.eventOversized || !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	part := bytes.TrimPrefix(line[5:], []byte(" "))
	if len(b.eventData)+len(part)+1 > codexDiagnosticLimit {
		b.eventOversized = true
		b.truncated = true
		b.eventData = nil
		return
	}
	if len(b.eventData) > 0 {
		b.eventData = append(b.eventData, '\n')
	}
	b.eventData = append(b.eventData, part...)
}

func (b *codexDiagnosticBody) observeJSON(data []byte) {
	if b.inspectError != nil {
		b.inspectError(data)
	}
	if !gjson.ValidBytes(data) {
		return
	}
	event := gjson.GetBytes(data, "type").String()
	switch event {
	case "response.output_text.delta", "response.function_call_arguments.delta", "response.reasoning_summary_text.delta":
		if !b.firstOutput {
			b.firstOutput = true
			b.emit("http_first_output", zap.Int64("first_output_ms", time.Since(b.start).Milliseconds()))
		}
	case "response.completed", "response.failed", "response.incomplete", "error":
		b.terminal = event
		b.errorClass = diagnosticErrorClass(b.status, data)
		if event == "response.failed" || event == "error" {
			if b.errorClass == "none" {
				b.errorClass = "other_upstream_error"
			}
		}
	}
}

func (b *codexDiagnosticBody) finishLocked(err error) {
	if b.done {
		return
	}
	b.done = true
	if b.sse {
		if len(b.buf) > 0 || b.oversized {
			b.consumeLine()
		}
		if !b.eventOversized {
			b.observeJSON(b.eventData)
		}
		b.eventData = nil
	}
	if !b.sse {
		if b.inspectError != nil {
			b.inspectError(b.buf)
		}
		b.errorClass = diagnosticErrorClass(b.status, b.buf)
	}
	if b.errorClass == "" {
		b.errorClass = diagnosticErrorClass(b.status, nil)
	}
	outcome := "closed"
	if errors.Is(err, io.EOF) {
		outcome = "eof"
	} else if err != nil {
		outcome = "read_error"
	}
	if b.ctx != nil && b.ctx.Err() != nil {
		if errors.Is(b.ctx.Err(), context.Canceled) {
			outcome = "canceled"
		} else {
			outcome = "timeout"
		}
	}
	b.emit("http_body_end", zap.Int("upstream_status", b.status), zap.String("outcome", outcome), zap.String("terminal_event", b.terminal), zap.String("error_class", b.errorClass), zap.Bool("observation_truncated", b.truncated), zap.Int64("elapsed_ms", time.Since(b.start).Milliseconds()))
	b.buf = nil
}

func (b *codexDiagnosticBody) Close() error {
	err := b.ReadCloser.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finishLocked(nil)
	return err
}

func (s *OpenAIGatewayService) observeCodexWSFrame(c *gin.Context, account *Account, frame []byte, outbound bool, boundary ...string) {
	if c == nil || c.Request == nil {
		return
	}
	fields := s.codexDiagnosticFields(c.Request.Context(), account)
	if len(fields) == 0 {
		return
	}
	if len(frame) > codexDiagnosticLimit {
		emitCodexDiagnostic(fields, "ws_observation_truncated", zap.Bool("outbound", outbound))
		return
	}
	event := gjson.GetBytes(frame, "type").String()
	switch event {
	case "response.create", "response.metadata", "response.completed", "response.failed", "response.incomplete", "error":
	default:
		return
	}
	phase := "upstream_receive"
	if outbound {
		phase = "upstream_send_attempt"
	}
	if len(boundary) > 0 {
		phase = boundary[0]
	}
	fields = append(fields, zap.String("boundary", phase), zap.String("transport", "websocket"), zap.Bool("outbound", outbound), zap.String("frame_event", event), zap.String("session_ref", codexDiagnosticHash(extractClientSessionID(c.Request.Header))))
	if outbound {
		fields = append(fields, diagnosticStateFields("sent_state", gjson.GetBytes(frame, "client_metadata.x-codex-turn-state").String())...)
		fields = append(fields, zap.String("model", diagnosticModel(gjson.GetBytes(frame, "model").String())), zap.String("reasoning_effort", diagnosticEffort(gjson.GetBytes(frame, "reasoning.effort").String())))
	}
	if event == "response.metadata" {
		gjson.GetBytes(frame, "headers").ForEach(func(k, v gjson.Result) bool {
			if strings.EqualFold(k.String(), openAICodexTurnStateHeader) {
				fields = append(fields, diagnosticStateFields("received_state", v.String())...)
			}
			return true
		})
	}
	if event == "error" || event == "response.failed" {
		fields = append(fields, zap.String("error_class", diagnosticErrorClass(0, frame)))
	}
	emitCodexDiagnostic(fields, "ws_frame")
}
