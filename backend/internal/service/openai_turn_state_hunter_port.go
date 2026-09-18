// SPDX-License-Identifier: LGPL-3.0-only
// Ported from KlN-4096/sub2api v0.2.5-klno.12, 2916a74b31353575f4901659af99477caf0f72df.
// Pure probe identity, backoff and state helpers; global policy/storage live in the CallAI adapter.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

const (
	openAITurnStateHuntLastKeep     = 10
	openAITurnStateHuntExitsKeep    = 128
	openAITurnStateHuntExitCooldown = 7 * 24 * time.Hour
	openAITurnStateHuntExtraKey     = CodexTurnStateHuntKey
	ctxKeyTurnStateProbe            = "callai_turn_state_hunter_probe"
)

func openAITurnStateProbeContext(c *gin.Context) bool {
	return c != nil && c.GetBool(ctxKeyTurnStateProbe)
}

type openAITurnStateHuntAttempt struct {
	RetryAttempt int       `json:"retry_attempt,omitempty"`
	At           time.Time `json:"at"`
	Model        string    `json:"model"`
	ProxyID      int64     `json:"proxy_id"`
	Proxy        string    `json:"proxy"`
	Status       int       `json:"status"`
	Chars        int       `json:"chars"`
	Healthy      bool      `json:"healthy"`
	// LatencyMs 是响应头到手的耗时（实测 0.5–2.3s）：探测在这一刻就断，后面不再计时。
	LatencyMs int64 `json:"latency_ms"`
	// Exit 是探测前解析到的出口 IP，只有固定出口有；轮换端点由供应商按连接选出口，为空。
	Exit  string `json:"exit,omitempty"`
	Error string `json:"error,omitempty"`
}

// openAITurnStateHuntExit 记一个出口 IP 最近一次探测的结果，冷却判定的依据。
type openAITurnStateHuntExit struct {
	IP      string    `json:"ip"`
	ProxyID int64     `json:"proxy_id"`
	At      time.Time `json:"at"`
	Healthy bool      `json:"healthy"`
}

// openAITurnStateHuntState 是 extra.openai_turn_state_hunt 的形态，每次探测后写一次。
type openAITurnStateHuntState struct {
	Owner     string                       `json:"owner"`
	NextAt    time.Time                    `json:"next_at"`
	HourStart time.Time                    `json:"hour_start"`
	HourCount int                          `json:"hour_count"`
	Cursor    int                          `json:"cursor"`
	Last      []openAITurnStateHuntAttempt `json:"last"`
	Exits     []openAITurnStateHuntExit    `json:"exits,omitempty"`
	LastError string                       `json:"last_error,omitempty"`
	// CapWait 标记 NextAt 是「撞上限等窗」定的（而不是出错退避）：上限调高后本窗还有余量
	// 就不用等到点，立刻恢复。
	CapWait bool `json:"cap_wait,omitempty"`
	// Gate 记录上一次被门槛挡住的原因（idle / fresh），正在猎时为空。不留痕的话
	// 「票还新鲜」「无真实流量」「模型名配错」在页面上长得一模一样。
	Gate      string    `json:"gate,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	openAITurnStateHuntGateIdle  = "idle"
	openAITurnStateHuntGateFresh = "fresh"
)

// waiting 报告现在是否还该等：退避照等；撞上限的等待在上限调高后自动解除。
func (st *openAITurnStateHuntState) waiting(cfg TurnStateHunterSettings, now time.Time) bool {
	if !now.Before(st.NextAt) {
		return false
	}
	return !st.CapWait || st.HourCount >= cfg.PerAccountMaxPerHour
}

// noteExit 记录出口的最新结果（同一「代理 × IP」只留最新一条，最多 128 条，够 64 个固定
// 代理各换一次 IP）。两个代理共用一个出口时各留一条，这样下一轮两个代理都能不回声就跳过。
func (st *openAITurnStateHuntState) noteExit(attempt openAITurnStateHuntAttempt) {
	if attempt.Exit == "" || attempt.Error != "" {
		return
	}
	st.recordExit(openAITurnStateHuntExit{IP: attempt.Exit, ProxyID: attempt.ProxyID, At: attempt.At, Healthy: attempt.Healthy})
}

func (st *openAITurnStateHuntState) recordExit(entry openAITurnStateHuntExit) {
	kept := make([]openAITurnStateHuntExit, 0, len(st.Exits)+1)
	kept = append(kept, entry)
	for _, e := range st.Exits {
		if e.IP != entry.IP || e.ProxyID != entry.ProxyID {
			kept = append(kept, e)
		}
	}
	if len(kept) > openAITurnStateHuntExitsKeep {
		kept = kept[:openAITurnStateHuntExitsKeep]
	}
	st.Exits = kept
}

// exitCoolingDown 报告该出口是否在冷却期：最近一次铸的是 312 且不到 7 天。铸出 292 的出口
// 不冷却——它对别的模型也大概率是好出口。条目按新到旧排，第一条命中的就是最新结果。
func (st *openAITurnStateHuntState) exitCoolingDown(ip string, now time.Time) bool {
	for _, e := range st.Exits {
		if e.IP == ip {
			return !e.Healthy && now.Sub(e.At) < openAITurnStateHuntExitCooldown
		}
	}
	return false
}

func readOpenAITurnStateHuntState(a *Account) openAITurnStateHuntState {
	var st openAITurnStateHuntState
	if a == nil || a.Extra == nil {
		return st
	}
	raw, ok := a.Extra[openAITurnStateHuntExtraKey]
	if !ok || raw == nil {
		return st
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return openAITurnStateHuntState{}
	}
	if err := json.Unmarshal(encoded, &st); err != nil {
		return openAITurnStateHuntState{}
	}
	if st.Cursor < 0 || len(st.Last) > openAITurnStateHuntLastKeep || len(st.Exits) > openAITurnStateHuntExitsKeep || st.Owner != turnStateOwner(a) {
		return openAITurnStateHuntState{}
	}
	return st
}

func (st *openAITurnStateHuntState) rollHour(now time.Time) {
	if st.HourStart.IsZero() || !now.Before(st.HourStart.Add(time.Hour)) {
		st.HourStart = now
		st.HourCount = 0
	}
}

func (st *openAITurnStateHuntState) push(attempt openAITurnStateHuntAttempt) {
	st.Last = append([]openAITurnStateHuntAttempt{attempt}, st.Last...)
	if len(st.Last) > openAITurnStateHuntLastKeep {
		st.Last = st.Last[:openAITurnStateHuntLastKeep]
	}
	st.HourCount++
	st.LastError = attempt.Error
	st.UpdatedAt = attempt.At
	st.noteExit(attempt)
}

func openAITurnStateHuntJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	return base/2 + time.Duration(rand.Int64N(int64(base)))
}

func openAITurnStateHuntBackoff(status int) time.Duration {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return 6 * time.Hour // 凭据问题，猎手自己修不了；真实流量会触发刷新/停号
	case http.StatusTooManyRequests:
		return time.Hour
	default:
		return 15 * time.Minute
	}
}

type openAITurnStateProbeIdentity struct {
	session, thread, turn, window, contextWindow, installation string
}

func newOpenAITurnStateProbeIdentity(account *Account) openAITurnStateProbeIdentity {
	thread := uuid.Must(uuid.NewV7()).String()
	return openAITurnStateProbeIdentity{
		session:       thread,
		thread:        thread,
		turn:          uuid.Must(uuid.NewV7()).String(),
		window:        thread + ":1",
		contextWindow: uuid.Must(uuid.NewV7()).String(),
		installation:  deriveStableUUIDv4("turn-state-hunter:" + codexAccountIdentityNamespace(account)),
	}
}

func (ids openAITurnStateProbeIdentity) turnMetadata() string {
	encoded, _ := json.Marshal(map[string]any{
		"installation_id":   ids.installation,
		"session_id":        ids.session,
		"thread_id":         ids.thread,
		"turn_id":           ids.turn,
		"root_turn_id":      ids.turn,
		"window_id":         ids.window,
		"context_window_id": ids.contextWindow,
		"window_number":     1,
	})
	return string(encoded)
}

// newOpenAITurnStateProbeContext 合成一个入站上下文，形态照 Codex CLI 直连的第一回合
// （见 openai_codex_fingerprint_convergence_test.go 的 newConvTestContext）。管线的
// 头/体投影全从它读入站信息，所以这里必须像一个真客户端。
func newOpenAITurnStateProbeContext(ids openAITurnStateProbeIdentity) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	identity := resolveCodexOutboundIdentity("")
	h := c.Request.Header
	h.Set("User-Agent", identity.userAgent)
	h.Set("originator", identity.originator)
	h.Set("content-type", "application/json")
	h.Set("session-id", ids.session)
	h.Set("thread-id", ids.thread)
	h.Set("x-client-request-id", ids.thread)
	h.Set("x-codex-installation-id", ids.installation)
	h.Set("x-codex-window-id", ids.window)
	h.Set(openAIWSTurnMetadataHeader, ids.turnMetadata())
	// 探测必须自然铸造：不参与自动接管注入，也不算真实流量。
	// Probe marker skips injection and real-traffic accounting.
	c.Set(ctxKeyTurnStateProbe, true)
	return c
}

// openAITurnStateProbeBody 是最小的第一回合请求体：字段集照真实 Codex CLI，内容只有
// 一个 "hi"。instructions 用该模型的真实 base prompt（与真客户端一致；头到手即断，
// 成本只有这段输入的 token）。
func openAITurnStateProbeBody(model, effort string, ids openAITurnStateProbeIdentity) map[string]any {
	return map[string]any{
		"model":        model,
		"instructions": openai.CodexBaseInstructionsForModel(model),
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "hi"}},
		}},
		"tools":               []any{},
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"reasoning":           map[string]any{"effort": effort, "summary": "auto"},
		"store":               false,
		"stream":              true,
		"include":             []any{"reasoning.encrypted_content"},
		"prompt_cache_key":    ids.session,
		"client_metadata": map[string]any{
			"session_id":              ids.session,
			"thread_id":               ids.thread,
			"turn_id":                 ids.turn,
			"root_turn_id":            ids.turn,
			"x-codex-installation-id": ids.installation,
			"x-codex-window-id":       ids.window,
			"x-codex-turn-metadata":   ids.turnMetadata(),
		},
	}
}

// buildOpenAITurnStateProbe 走与 Forward 相同的 Codex OAuth 出站序列：身份来源 → OAuth
// 转换 → 身份收口暂存 → buildUpstreamRequest。返回的上下文供响应侧复用
// （relayOpenAICodexTurnState 要从它读模型、注入标记与铸造者）。
//
// 与真实流量的已知差异（刻意不补：铸什么只看账号权重与出口 IP）：没有入站 API Key，
// 身份来源按「无 key」派生；installation_id 是猎手自己的稳定派生值；请求体没有
// environment_context，头里没有 x-codex-inference-call-id；探测请求 req.Close=true，
// 走到 HTTP/1.1（openai_http2 关闭或 http 代理触发 H1 回退的 10 分钟内）时会多发一个
// Connection: close 头，H2 路径没有这条差异；探测直接走 httpUpstream，不经插件路径——
// 装了接管 oauth 出站的插件时，探测与真实流量的传输层/TLS 指纹不同（插件协议不带
// req.Close，走插件就换不了出口，两害取其轻）。
func (s *OpenAIGatewayService) buildOpenAITurnStateProbe(ctx context.Context, account *Account, model, effort string) (*gin.Context, *http.Request, error) {
	if s == nil || account == nil {
		return nil, nil, errors.New("turn-state probe: gateway or account is nil")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, nil, errors.New("turn-state probe: model is empty")
	}
	account = s.prepareCodexFingerprintAccount(ctx, account)
	ids := newOpenAITurnStateProbeIdentity(account)
	c := newOpenAITurnStateProbeContext(ids)
	if _, err := s.prepareCodexAccountIdentitySource(ctx, c, account); err != nil {
		return nil, nil, err
	}
	decoded := openAITurnStateProbeBody(model, effort, ids)
	result := applyCodexOAuthTransformWithOptions(decoded, codexOAuthTransformOptions{IsCodexCLI: true})
	if result.Error != nil {
		return nil, nil, result.Error
	}
	stageCodexOAuthIdentity(c, account, decoded, false)
	upstreamModel := model
	if result.NormalizedModel != "" {
		upstreamModel = result.NormalizedModel
	}
	SetOpsUpstreamModel(c, upstreamModel)
	body, err := json.Marshal(decoded)
	if err != nil {
		return nil, nil, err
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, nil, err
	}
	req, err := s.buildUpstreamRequest(ctx, c, account, body, token, true, ids.session, true)
	if err != nil {
		return nil, nil, err
	}
	return c, req, nil
}
