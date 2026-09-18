// SPDX-License-Identifier: LGPL-3.0-only
// Shape fixture and selection cases ported/adapted from KlN klno.9, 2b6600c0360b9.
package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func turnStateFernetBlob(minted time.Time, blocks int) string {
	raw := make([]byte, 1+8+16+blocks*16+32)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(minted.Unix()))
	return base64.URLEncoding.EncodeToString(raw)
}

func TestOpenAITurnStateShapeTable(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		blocks, chars int
		baseline      bool
	}{{10, 292, true}, {11, 312, false}, {12, 332, true}, {13, 356, false}, {14, 376, false}} {
		blob := turnStateFernetBlob(now, tc.blocks)
		require.Equal(t, tc.chars, len(blob))
		require.Equal(t, tc.baseline, openAITurnStateHealthy(blob))
		require.Equal(t, tc.baseline, openAITurnStateHealthy(strings.TrimRight(blob, "=")))
		env, ok := parseOpenAITurnStateEnvelope(blob)
		require.True(t, ok)
		require.Equal(t, tc.blocks, env.CipherBlocks)
	}
	for _, n := range []int{292, 332} {
		require.True(t, openAITurnStateHealthy(strings.Repeat("x", n)))
	}
	for _, n := range []int{0, 312, 356, 400} {
		require.False(t, openAITurnStateHealthy(strings.Repeat("x", n)))
	}
	_, ok := parseOpenAITurnStateEnvelope(turnStateFernetBlob(now.Add(48*time.Hour), 10))
	require.False(t, ok)
}

func TestPickTurnStateCandidate(t *testing.T) {
	now := time.Now()
	m := "gpt-test"
	pool := turnStatePool{Candidates: []turnStateCandidate{
		{Blob: "failed", Model: m, MintedAt: now, Failed: true},
		{Blob: "expired", Model: m, MintedAt: now.Add(-turnStateTTL)},
		{Blob: "wrong-model", Model: "other", MintedAt: now},
		{Blob: "fresh", Model: m, MintedAt: now.Add(-turnStateTTL + time.Nanosecond)},
	}}
	found, ok := pickTurnStateCandidate(pool, m, now)
	require.True(t, ok)
	require.Equal(t, "fresh", found.Blob)
	_, ok = pickTurnStateCandidate(pool, "missing", now)
	require.False(t, ok)
	_, ok = pickTurnStateCandidate(turnStatePool{}, m, now)
	require.False(t, ok)
}

type stateTestRepo struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
	fail    bool
	writes  int
}

func cloneStateAccount(a *Account) *Account {
	data, _ := json.Marshal(a)
	var c Account
	_ = json.Unmarshal(data, &c)
	return &c
}
func (r *stateTestRepo) ReadCodexTurnState(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return nil, errors.New("private error")
	}
	if r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	return cloneStateAccount(r.account), nil
}
func (r *stateTestRepo) MutateCodexTurnState(_ context.Context, id int64, fn func(*Account) (map[string]any, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("private error")
	}
	if r.account.ID != id {
		return ErrAccountNotFound
	}
	updates, err := fn(cloneStateAccount(r.account))
	if err != nil {
		return err
	}
	for k, v := range updates {
		r.account.Extra[k] = v
	}
	if len(updates) > 0 {
		r.writes++
	}
	return nil
}

type stateTestCipher struct{}

func (stateTestCipher) Encrypt(v string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(v)), nil
}
func (stateTestCipher) Decrypt(v string) (string, error) {
	b, e := base64.StdEncoding.DecodeString(v)
	return string(b), e
}

func stateAutoTest(t *testing.T) (*OpenAIGatewayService, *stateTestRepo, *openAIOAuthRuntimeSettingRepo, *Account, *bytes.Buffer) {
	t.Helper()
	cfg := &config.Config{Totp: config.TotpConfig{EncryptionKeyConfigured: true, EncryptionKey: strings.Repeat("42", 32)}}
	settings := newOpenAIOAuthRuntimeSettingRepo()
	settings.values[SettingKeyOpenAIOAuthRuntimeSettings] = `{"openai_oauth_turn_state_auto_enabled":true}`
	a := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "private-account"}, Extra: map[string]any{}, Schedulable: true}
	repo := &stateTestRepo{account: cloneStateAccount(a)}
	var logs bytes.Buffer
	svc := &OpenAIGatewayService{cfg: cfg, settingService: NewSettingService(settings, cfg), accountRepo: repo, turnStateEncryptor: stateTestCipher{}}
	svc.turnStateLog.writeOverride = logs.Write
	return svc, repo, settings, a, &logs
}
func stateBoundRequest(s *OpenAIGatewayService, a *Account, tenant int64, session, model string) *http.Request {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("session-id", session)
	c.Set("api_key", &APIKey{ID: tenant})
	req := httptest.NewRequest("POST", "https://upstream.invalid/responses", nil)
	return s.bindTurnStateAttempt(req, c, a, []byte(`{"model":"`+model+`"}`))
}

func TestTurnStateAutoObserveInjectRejectAndDisable(t *testing.T) {
	s, repo, settings, a, logs := stateAutoTest(t)
	healthy := turnStateFernetBlob(time.Now().Add(-time.Minute), 12)
	long := turnStateFernetBlob(time.Now(), 13)
	seed := stateBoundRequest(s, a, 1, "seed", "gpt-test")
	s.prepareTurnStateHTTP(seed)
	s.recordTurnStateObservation(seed.Context(), turnStateAttemptFrom(seed.Context()), healthy)
	public := CodexTurnStatePublicExtra(repo.account)
	require.Contains(t, public, CodexTurnStateSummaryKey)
	publicJSON, publicErr := json.Marshal(public)
	require.NoError(t, publicErr)
	require.NotContains(t, string(publicJSON), healthy)
	require.NotContains(t, string(publicJSON), repo.account.Extra[CodexTurnStatePoolKey])
	require.NotContains(t, string(publicJSON), turnStateOwner(a))
	mark := stateBoundRequest(s, a, 1, "affected", "gpt-test")
	s.prepareTurnStateHTTP(mark)
	s.recordTurnStateObservation(mark.Context(), turnStateAttemptFrom(mark.Context()), long)
	req := stateBoundRequest(s, a, 1, "affected", "gpt-test")
	s.prepareTurnStateHTTP(req)
	require.Equal(t, healthy, req.Header.Get(openAICodexTurnStateHeader))
	s.recordTurnStateObservation(req.Context(), turnStateAttemptFrom(req.Context()), long)
	pool, err := s.decodeTurnStatePool(repo.account)
	require.NoError(t, err)
	require.False(t, pool.Candidates[0].Failed, "longer response is not rejection")
	resp := &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_encrypted_content","message":"private response"}}`))}
	s.observeTurnStateHTTP(req, resp, nil)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "private response")
	require.NoError(t, resp.Body.Close())
	pool, err = s.decodeTurnStatePool(repo.account)
	require.NoError(t, err)
	require.True(t, pool.Candidates[0].Failed)
	require.True(t, repo.account.Schedulable)
	retry := stateBoundRequest(s, a, 1, "affected", "gpt-test")
	s.prepareTurnStateHTTP(retry)
	require.Empty(t, retry.Header.Get(openAICodexTurnStateHeader))
	settings.values[SettingKeyOpenAIOAuthRuntimeSettings] = `{"openai_oauth_turn_state_auto_enabled":false}`
	off := stateBoundRequest(s, a, 1, "affected", "gpt-test")
	s.prepareTurnStateHTTP(off)
	require.False(t, turnStateAttemptFrom(off.Context()).Enabled)
	require.NotContains(t, logs.String(), healthy)
	require.NotContains(t, logs.String(), "private-account")
	require.NotContains(t, logs.String(), "private response")
	require.Contains(t, logs.String(), "candidate_rejected")
	require.Contains(t, logs.String(), "continue_normal_scheduling")
}

func TestTurnStateIsolationIdentityAndStorageFailure(t *testing.T) {
	s, repo, _, a, _ := stateAutoTest(t)
	req := stateBoundRequest(s, a, 1, "shared", "gpt-test")
	one := turnStateAttemptFrom(req.Context())
	otherTenant := turnStateAttemptFrom(stateBoundRequest(s, a, 2, "shared", "gpt-test").Context())
	require.NotEqual(t, one.Session, otherTenant.Session)
	otherModel := turnStateAttemptFrom(stateBoundRequest(s, a, 1, "shared", "gpt-other").Context())
	require.NotEqual(t, one.Session, otherModel.Session)
	noSession := turnStateAttemptFrom(stateBoundRequest(s, a, 1, "", "gpt-test").Context())
	noSession2 := turnStateAttemptFrom(stateBoundRequest(s, a, 2, "", "gpt-test").Context())
	require.NotEqual(t, noSession.Session, noSession2.Session)
	s.turnStateSessions.set(one.Session, true, time.Now())
	repo.fail = true
	s.prepareTurnStateHTTP(req)
	require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
	repo.fail = false
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
	c.Request.Header.Set("Upgrade", "websocket")
	require.Same(t, req, s.bindTurnStateAttempt(req, c, a, []byte(`{"model":"gpt-test"}`)))
	c.Request.Header.Del("Upgrade")
	c.Set("callai_turn_state_ws", true)
	require.Same(t, req, s.bindTurnStateAttempt(req, c, a, []byte(`{"model":"gpt-test"}`)))
}

func TestTurnStatePoolCapsAndExpiryFallback(t *testing.T) {
	s, repo, _, a, _ := stateAutoTest(t)
	for n := 0; n < 20; n++ {
		req := stateBoundRequest(s, a, 1, "seed", "gpt-"+strings.Repeat("x", n+1))
		s.prepareTurnStateHTTP(req)
		s.recordTurnStateObservation(req.Context(), turnStateAttemptFrom(req.Context()), strings.Repeat("a", 332))
	}
	pool, err := s.decodeTurnStatePool(repo.account)
	require.NoError(t, err)
	require.Len(t, pool.Candidates, 16)
	for _, v := range pool.Candidates {
		require.False(t, v.MintedAt.IsZero())
	}
	oldOwner := pool.Owner
	repo.account.Credentials["chatgpt_account_id"] = "replacement"
	pool, err = s.decodeTurnStatePool(repo.account)
	require.NoError(t, err)
	require.Empty(t, pool.Candidates)
	require.NotEqual(t, oldOwner, pool.Owner)
	public := CodexTurnStatePublicExtra(repo.account)
	require.NotContains(t, public, CodexTurnStatePoolKey)
	require.NotContains(t, public, CodexTurnStateSummaryKey)
}

func TestTurnStateRuntimeRequiresEncryptionAndIsIndependent(t *testing.T) {
	repo := newOpenAIOAuthRuntimeSettingRepo()
	s := NewSettingService(repo, nil)
	yes := true
	_, err := s.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, nil, nil, nil, nil, nil, &yes)
	require.Error(t, err)
	cfg := &config.Config{Totp: config.TotpConfig{EncryptionKeyConfigured: true, EncryptionKey: strings.Repeat("42", 32)}}
	s = NewSettingService(repo, cfg)
	got, err := s.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, nil, nil, nil, nil, nil, &yes)
	require.NoError(t, err)
	require.True(t, got.TurnStateAutoEnabled)
	no := false
	got, err = s.UpdateOpenAIOAuthRuntimeSettings(context.Background(), &no, nil)
	require.NoError(t, err)
	require.True(t, got.TurnStateAutoEnabled)
}

func TestTurnStateLogFailureIsVisibleAndNonFatal(t *testing.T) {
	s, _, _, a, _ := stateAutoTest(t)
	s.turnStateLog.writeOverride = func([]byte) (int, error) { return 0, errors.New("private path") }
	req := stateBoundRequest(s, a, 1, "test", "gpt-test")
	s.prepareTurnStateHTTP(req)
	require.Positive(t, s.turnStateLog.failures.Load())
	require.Equal(t, 100, s.turnStateLog.writer.MaxSize)
	require.Equal(t, 10, s.turnStateLog.writer.MaxBackups)
	require.Equal(t, 7, s.turnStateLog.writer.MaxAge)
	require.True(t, s.turnStateLog.writer.Compress)
}

type turnStateEchoUpstream struct {
	HTTPUpstream
	client *http.Client
}

func (u turnStateEchoUpstream) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(r)
}

func TestTurnStateActualWireAndClientIsolation(t *testing.T) {
	s, _, _, a, logs := stateAutoTest(t)
	token := turnStateFernetBlob(time.Now(), 12)
	seed := stateBoundRequest(s, a, 1, "seed", "gpt-test")
	s.prepareTurnStateHTTP(seed)
	s.recordTurnStateObservation(seed.Context(), turnStateAttemptFrom(seed.Context()), token)
	marked := stateBoundRequest(s, a, 1, "affected", "gpt-test")
	s.prepareTurnStateHTTP(marked)
	s.recordTurnStateObservation(marked.Context(), turnStateAttemptFrom(marked.Context()), turnStateFernetBlob(time.Now(), 13))
	seen := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get(openAICodexTurnStateHeader)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()
	s.httpUpstream = turnStateEchoUpstream{client: upstream.Client()}
	for _, tenant := range []int64{1, 2} {
		req := stateBoundRequest(s, a, tenant, "affected", "gpt-test")
		endpoint, err := url.Parse(upstream.URL)
		require.NoError(t, err)
		req.URL = endpoint
		req.RequestURI = ""
		resp, err := s.doOpenAIUpstream(req, "", a)
		require.NoError(t, err)
		_, err = io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	require.Equal(t, token, <-seen)
	require.Empty(t, <-seen)
	require.Contains(t, logs.String(), "http_body_end")
	require.Contains(t, logs.String(), "response.completed")
	require.NotContains(t, logs.String(), token)
}

func TestTurnStateRejectedEvictedTokenDoesNotRevive(t *testing.T) {
	s, repo, _, a, _ := stateAutoTest(t)
	token := turnStateFernetBlob(time.Now().Add(-time.Minute), 12)
	req := stateBoundRequest(s, a, 1, "session", "gpt-test")
	s.prepareTurnStateHTTP(req)
	attempt := turnStateAttemptFrom(req.Context())
	s.recordTurnStateObservation(req.Context(), attempt, token)
	attempt.Injected = token
	s.rejectTurnStateCandidate(req.Context(), attempt)
	// Simulate the old token being evicted; the rejection ledger survives.
	pool, err := s.decodeTurnStatePool(repo.account)
	require.NoError(t, err)
	pool.Candidates = nil
	updates, err := s.encodeTurnStatePool(pool, time.Now())
	require.NoError(t, err)
	for k, v := range updates {
		repo.account.Extra[k] = v
	}
	next := stateBoundRequest(s, a, 1, "other", "gpt-test")
	s.prepareTurnStateHTTP(next)
	s.recordTurnStateObservation(next.Context(), turnStateAttemptFrom(next.Context()), token)
	pool, err = s.decodeTurnStatePool(repo.account)
	require.NoError(t, err)
	require.Empty(t, pool.Candidates)
	require.NotEmpty(t, pool.Rejected)
}

func TestTurnStateShadowCredentialOwnershipAndBoundedSessions(t *testing.T) {
	s, _, _, parent, _ := stateAutoTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 1})
	shadow := &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent.ID}
	c.Set(codexAccountIdentitySourceContextKey, parent)
	req := s.bindTurnStateAttempt(httptest.NewRequest("POST", "https://upstream.invalid/responses", nil), c, shadow, []byte(`{"model":"gpt-test"}`))
	attempt := turnStateAttemptFrom(req.Context())
	require.NotNil(t, attempt)
	require.Equal(t, parent.ID, attempt.AccountID)
	require.Equal(t, turnStateOwner(parent), attempt.Owner)
	now := time.Now()
	for i := 0; i < 9000; i++ {
		s.turnStateSessions.set(strconv.Itoa(i), true, now.Add(time.Duration(i)*time.Nanosecond))
	}
	require.LessOrEqual(t, len(s.turnStateSessions.values), 8192)
	require.False(t, s.turnStateSessions.needs("8999", now.Add(2*time.Hour)))
}

func TestTurnStateIndependentLogWritesAtWarn(t *testing.T) {
	before := logger.CurrentLevel()
	require.NoError(t, logger.Init(logger.InitOptions{Level: "warn", Format: "json", Output: logger.OutputOptions{ToStdout: true}}))
	require.NoError(t, logger.SetLevel("warn"))
	t.Cleanup(func() { require.NoError(t, logger.SetLevel(before)) })
	s, _, _, account, logs := stateAutoTest(t)
	req := stateBoundRequest(s, account, 1, "test", "gpt-test")
	s.prepareTurnStateHTTP(req)
	require.Contains(t, logs.String(), `"event":"skip"`)
	require.Equal(t, "warn", logger.CurrentLevel())
}

func TestTurnStateRejectionIsolatedBetweenModels(t *testing.T) {
	s, repo, _, a, _ := stateAutoTest(t)
	token := turnStateFernetBlob(time.Now().Add(-time.Minute), 12)
	nonBaseline := turnStateFernetBlob(time.Now(), 13)
	for _, model := range []string{"model-a", "model-b"} {
		seed := stateBoundRequest(s, a, 1, "seed", model)
		s.prepareTurnStateHTTP(seed)
		s.recordTurnStateObservation(seed.Context(), turnStateAttemptFrom(seed.Context()), token)
		mark := stateBoundRequest(s, a, 1, "affected", model)
		s.prepareTurnStateHTTP(mark)
		s.recordTurnStateObservation(mark.Context(), turnStateAttemptFrom(mark.Context()), nonBaseline)
	}
	first := stateBoundRequest(s, a, 1, "affected", "model-a")
	s.prepareTurnStateHTTP(first)
	require.Equal(t, token, first.Header.Get(openAICodexTurnStateHeader))
	s.rejectTurnStateCandidate(first.Context(), turnStateAttemptFrom(first.Context()))
	// Both the persisted rejection ledger and the local immediate retry guard
	// must keep the identical bytes usable for the other model.
	pool, err := s.decodeTurnStatePool(repo.account)
	require.NoError(t, err)
	_, usable := pickTurnStateCandidate(pool, "model-a", time.Now())
	require.False(t, usable)
	_, usable = pickTurnStateCandidate(pool, "model-b", time.Now())
	require.True(t, usable)
	second := stateBoundRequest(s, a, 1, "affected", "model-b")
	s.prepareTurnStateHTTP(second)
	require.Equal(t, token, second.Header.Get(openAICodexTurnStateHeader))
	retry := stateBoundRequest(s, a, 1, "affected", "model-a")
	s.prepareTurnStateHTTP(retry)
	require.Empty(t, retry.Header.Get(openAICodexTurnStateHeader))
}
