// SPDX-License-Identifier: LGPL-3.0-only
// Candidate and session algorithm adapted from KlN-4096/sub2api v0.2.5-klno.9,
// commit 2b6600c0360b9eefb60e91c4282b83ec385fadf1. See CODEX_TURN_STATE_AUTO.md.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	CodexTurnStatePoolKey        = "openai_turn_state_pool"
	CodexTurnStateObservationKey = "openai_turn_state_observed"
	CodexTurnStateSummaryKey     = "openai_turn_state_summary"
	turnStateTTL                 = time.Hour
	turnStateTimeout             = 250 * time.Millisecond
	turnStateMaxModels           = 16
	turnStatePoolSize            = 3
	turnStateMaxBlob             = 4096
)

// The callback runs against the latest account under a DB row lock. Implementors
// persist only these managed extra keys, without changing scheduler state.
type CodexTurnStateStore interface {
	ReadCodexTurnState(context.Context, int64) (*Account, error)
	MutateCodexTurnState(context.Context, int64, func(*Account) (map[string]any, error)) error
}

type turnStateCandidate struct {
	Blob     string    `json:"blob"`
 Source string `json:"source,omitempty"`
	Model    string    `json:"model"`
	MintedAt time.Time `json:"minted_at"`
	Failed   bool      `json:"failed,omitempty"`
}
type turnStatePool struct {
	Owner      string               `json:"owner"`
	Candidates []turnStateCandidate `json:"candidates"`
	Rejected   map[string]time.Time `json:"rejected,omitempty"`
}

// Like klno.9, candidates are model-specific, newest first, and expiry is a hard
// selection boundary. Unlike its zero-time fallback, persisted entries always
// carry either the parsed issuance time or their actual observation time.
func (c turnStateCandidate) usable(model string, now time.Time) bool {
	return !c.Failed && c.Blob != "" && c.Model == model && !c.MintedAt.IsZero() && !c.MintedAt.After(now.Add(24*time.Hour)) && now.Before(c.MintedAt.Add(turnStateTTL))
}
func pickTurnStateCandidate(pool turnStatePool, model string, now time.Time) (turnStateCandidate, bool) {
	for _, entry := range pool.Candidates {
		if entry.usable(model, now) && !now.Before(pool.Rejected[turnStateRejectionKey(entry.Model, entry.Blob)]) {
			return entry, true
		}
	}
	return turnStateCandidate{}, false
}

// Rejection attribution has the same model boundary as candidate selection.
func turnStateRejectionKey(model, value string) string {
	sum := sha256.Sum256([]byte(turnStateModel(model) + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func turnStateOwner(a *Account) string {
	if a == nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeOAuth || a.IsCredentialShadow() {
		return ""
	}
	namespace := codexAccountIdentityNamespace(a)
	if namespace == "" || strings.HasPrefix(namespace, "seed:") {
		return ""
	}
	sum := sha256.Sum256([]byte(namespace))
	return hex.EncodeToString(sum[:])
}
func turnStateModel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, c := range value {
		if c < ' ' || c > 126 {
			return ""
		}
	}
	return value
}

type turnStateSession struct {
	Needs   bool
	Expires time.Time
}
type turnStateSessions struct {
	mu     sync.Mutex
	values map[string]turnStateSession
}

func (s *turnStateSessions) needs(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	if ok && !now.Before(v.Expires) {
		delete(s.values, key)
		return false
	}
	return ok && v.Needs
}
func (s *turnStateSessions) set(key string, needs bool, now time.Time) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]turnStateSession{}
	}
	if len(s.values) >= 8192 {
		var oldest string
		var at time.Time
		for k, v := range s.values {
			if !now.Before(v.Expires) {
				delete(s.values, k)
				continue
			}
			if oldest == "" || v.Expires.Before(at) {
				oldest = k
				at = v.Expires
			}
		}
		if len(s.values) >= 8192 {
			delete(s.values, oldest)
		}
	}
	s.values[key] = turnStateSession{Needs: needs, Expires: now.Add(turnStateTTL)}
}

type turnStateAttemptKey struct{}
type turnStateAttempt struct {
	StartedAt                         time.Time
	ID                                uint64
	AccountID                         int64
	Owner, Model, Session, RequestRef string
	Original, Sent, Injected          string
	Enabled                           bool
 Probe bool
 CandidateSource string
	Rejected                          atomic.Bool
	Observed                          atomic.Bool
}

var turnStateAttemptSequence atomic.Uint64

func (s *OpenAIGatewayService) bindTurnStateAttempt(req *http.Request, c *gin.Context, a *Account, body []byte) *http.Request {
	if s == nil || req == nil || c == nil || c.Request == nil || a == nil {
		return req
	}
	// Includes WS ingress's HTTP bridge and HTTP requests routed through WS fallback.
	if _, ok := c.Get("callai_turn_state_ws"); ok {
		return req
	}
	if strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		return req
	}
	source := codexAccountIdentitySource(c, a)
	owner := turnStateOwner(source)
	if owner == "" {
		if source != nil && source.Platform == PlatformOpenAI && source.Type == AccountTypeOAuth {
			s.logTurnState(&turnStateAttempt{AccountID: source.ID, Owner: "unresolved:" + strconv.FormatInt(source.ID, 10), Model: turnStateModel(gjson.GetBytes(body, "model").String()), RequestRef: turnStateRequestRef(req.Context())}, "skip", "upstream_identity_unavailable", nil)
		}
		return req
	}
	model := turnStateModel(gjson.GetBytes(body, "model").String())
	if model == "" {
		model = turnStateModel(c.GetString(OpsUpstreamModelKey))
	}
	if model == "" {
		return req
	}
	rawSession := extractClientSessionID(c.Request.Header)
	if rawSession == "" {
		rawSession = "\x00no-session"
	}
	tenant := strconv.FormatInt(getAPIKeyIDFromContext(c), 10)
	// API key IDs are globally user-owned; absent keys get request-local isolation.
	if tenant == "0" {
		tenant = codexDiagnosticHash(strconv.FormatUint(turnStateAttemptSequence.Add(1), 10))
	}
	session := codexDiagnosticHash(owner + "\x00" + tenant + "\x00" + rawSession + "\x00" + model)
	attempt := &turnStateAttempt{ID: turnStateAttemptSequence.Add(1), AccountID: source.ID, Owner: owner, Model: model, Session: session, Original: req.Header.Get(openAICodexTurnStateHeader)}
	attempt.RequestRef = turnStateRequestRef(req.Context())
 attempt.Probe = openAITurnStateProbeContext(c)
	return req.WithContext(context.WithValue(req.Context(), turnStateAttemptKey{}, attempt))
}

func turnStateAttemptFrom(ctx context.Context) *turnStateAttempt {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(turnStateAttemptKey{}).(*turnStateAttempt)
	return v
}

func (s *OpenAIGatewayService) turnStateAutoEnabled(ctx context.Context) bool {
	if s == nil || s.settingService == nil || s.turnStateEncryptor == nil || s.cfg == nil || !s.cfg.Totp.EncryptionKeyConfigured {
		return false
	}
	// A fresh bounded read prevents a stale enabled cache from injecting after an
	// administrator turned the feature off on another process. Read failures close.
	op, cancel := context.WithTimeout(ctx, turnStateTimeout)
	defer cancel()
	policy, err := s.settingService.readOpenAIOAuthRuntimeSettings(op)
	return err == nil && policy.TurnStateAutoEnabled
}

func (s *OpenAIGatewayService) prepareTurnStateHTTP(req *http.Request) {
	a := turnStateAttemptFrom(req.Context())
	if a == nil {
		return
	}
	a.StartedAt = time.Now()
 if a.Probe { return }
 s.turnStateTraffic.note(a.AccountID,a.Model,a.StartedAt)
	a.Enabled = s.turnStateAutoEnabled(req.Context())
	a.Sent = a.Original
	if !a.Enabled {
		s.logTurnState(a, "skip", "global_off_or_unavailable", nil)
		return
	}
	policy, _ := s.hunterPolicy(req.Context())
 if !s.turnStateSessions.needs(a.Session, time.Now()) && !policy.hunts(a.Model) {
		s.logTurnState(a, "skip", "session_not_marked", nil)
		return
	}
	store, ok := s.accountRepo.(CodexTurnStateStore)
	if !ok {
		s.logTurnState(a, "skip", "storage_unavailable", nil)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), turnStateTimeout)
	defer cancel()
	latest, err := store.ReadCodexTurnState(ctx, a.AccountID)
	if err != nil || turnStateOwner(latest) != a.Owner {
		s.logTurnState(a, "skip", "storage_or_identity_changed", nil)
		return
	}
	pool, err := s.decodeTurnStatePool(latest)
	if err != nil {
		s.logTurnState(a, "skip", "pool_decrypt_failed", nil)
		return
	}
	// Persisted tombstones prevent an evicted/reobserved rejected token from reviving.
	for i := range pool.Candidates {
		if s.turnStateSessions.needs("rejected:"+a.Owner+":"+turnStateRejectionKey(pool.Candidates[i].Model, pool.Candidates[i].Blob), time.Now()) {
			pool.Candidates[i].Failed = true
		}
	}
	candidate, found := pickTurnStateCandidate(pool, a.Model, time.Now())
	if !found {
		s.logTurnState(a, "skip", "no_live_candidate", nil)
		return
	}
	a.Injected = candidate.Blob
 a.CandidateSource=candidate.Source
 if a.CandidateSource=="" {a.CandidateSource="natural"}
	a.Sent = candidate.Blob
	req.Header.Set(openAICodexTurnStateHeader, candidate.Blob)
	s.logTurnState(a, "inject", "candidate_selected", map[string]any{"candidate_age_seconds": int64(time.Since(candidate.MintedAt).Seconds())})
}

func (s *OpenAIGatewayService) decodeTurnStatePool(account *Account) (turnStatePool, error) {
	pool := turnStatePool{Owner: turnStateOwner(account)}
	if pool.Owner == "" {
		return pool, errors.New("missing credential owner")
	}
	raw, _ := account.Extra[CodexTurnStatePoolKey].(string)
	if raw == "" {
		return pool, nil
	}
	if s.turnStateEncryptor == nil {
		return pool, errors.New("missing encryptor")
	}
	plaintext, err := s.turnStateEncryptor.Decrypt(raw)
	if err != nil {
		return pool, errors.New("pool decryption failed")
	}
	var stored turnStatePool
	if json.Unmarshal([]byte(plaintext), &stored) != nil || len(stored.Candidates) > turnStateMaxModels*turnStatePoolSize || len(stored.Rejected) > 128 {
		return pool, errors.New("invalid pool")
	}
	if stored.Owner != pool.Owner {
		return pool, nil
	}
	return stored, nil
}

func (s *OpenAIGatewayService) observeTurnStateHTTP(req *http.Request, resp *http.Response, err error) {
	a := turnStateAttemptFrom(req.Context())
	if a == nil {
		return
	}
	if err != nil {
		s.logTurnState(a, "transport_end", "transport_error", nil)
		return
	}
	if resp == nil {
		return
	}
	value := extractOpenAICodexTurnState(resp.Header)
	fields := turnStateShapeFields(value)
	fields["http_status"] = resp.StatusCode
	if !a.StartedAt.IsZero() {
		fields["headers_ms"] = time.Since(a.StartedAt).Milliseconds()
	}
	s.logTurnState(a, "response_headers", "observed", fields)
	if len(value) > 0 && len(value) <= turnStateMaxBlob && a.Observed.CompareAndSwap(false, true) {
		s.recordTurnStateObservation(req.Context(), a, value)
	}
	// Passive bounded inspection shares the proven non-consuming body observer.
	if resp.Body != nil {
		resp.Body = &codexDiagnosticBody{ReadCloser: resp.Body, ctx: req.Context(), start: a.StartedAt, status: resp.StatusCode, sse: strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"), emit: func(event string, fields ...zap.Field) {
			enc := zapcore.NewMapObjectEncoder()
			for _, f := range fields {
				f.AddTo(enc)
			}
			s.logTurnState(a, event, "upstream_observation", enc.Fields)
		}, inspectError: func(payload []byte) {
			if !gjson.ValidBytes(payload) {
				return
			}
			if gjson.GetBytes(payload, "error.code").String() == "invalid_encrypted_content" || gjson.GetBytes(payload, "response.error.code").String() == "invalid_encrypted_content" {
				s.rejectTurnStateCandidate(req.Context(), a)
			}
		}}
	}
}

func (s *OpenAIGatewayService) recordTurnStateObservation(parent context.Context, a *turnStateAttempt, value string) {
	healthy := openAITurnStateHealthy(value)
	maintain := a.Enabled && s.turnStateAutoEnabled(context.WithoutCancel(parent))
	// Session state only follows natural (not injected) responses, matching klno.9.
	if maintain && a.Injected == "" && !a.Probe {
		s.turnStateSessions.set(a.Session, !healthy, time.Now())
	}
	store, ok := s.accountRepo.(CodexTurnStateStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), turnStateTimeout)
	defer cancel()
	action := "unchanged"
	err := store.MutateCodexTurnState(ctx, a.AccountID, func(latest *Account) (map[string]any, error) {
		if turnStateOwner(latest) != a.Owner {
			action = "identity_changed"
			return nil, nil
		}
		updates := map[string]any{}
		if a.Injected == "" && !a.Probe {
			prev, _ := latest.Extra[CodexTurnStateObservationKey].(map[string]any)
			last, _ := prev["observed_at"].(string)
			at, _ := time.Parse(time.RFC3339Nano, last)
			shape := turnStateShapeFields(value)
			if prev["shape"] != shape["shape"] || time.Since(at) >= 5*time.Minute {
				shape["observed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
				shape["model"] = a.Model
				shape["owner"] = a.Owner
				updates[CodexTurnStateObservationKey] = shape
				action = "shape_updated"
			}
		}
		if !maintain || !healthy {
			return updates, nil
		}
		pool, decodeErr := s.decodeTurnStatePool(latest)
		if decodeErr != nil {
			return nil, decodeErr
		}
		now := time.Now().UTC()
		for key, expires := range pool.Rejected {
			if !now.Before(expires) {
				delete(pool.Rejected, key)
			}
		}
		if now.Before(pool.Rejected[turnStateRejectionKey(a.Model, value)]) || len(pool.Rejected) >= 128 {
			return updates, nil
		}
		for _, candidate := range pool.Candidates {
			if candidate.Blob == value && candidate.Model == a.Model {
				return updates, nil
			}
		}
		minted := openAITurnStateMintedAt(value, now)
		if !now.Before(minted.Add(turnStateTTL)) {
			return updates, nil
		}
		source:="natural";if a.Probe{source="hunter"}
		pool.Candidates = append([]turnStateCandidate{{Blob: value, Model: a.Model, MintedAt: minted,Source:source}}, pruneTurnStatePool(pool.Candidates, a.Model, now)...)
		encoded, encodeErr := s.encodeTurnStatePool(pool, now)
		if encodeErr != nil {
			return nil, encodeErr
		}
		for key, v := range encoded {
			updates[key] = v
		}
		action = "candidate_added"
		return updates, nil
	})
	if err != nil {
		s.logTurnState(a, "storage_error", "observation_or_pool_update_failed", nil)
	} else {
		s.logTurnState(a, "observe", action, turnStateShapeFields(value))
	}
}

func pruneTurnStatePool(pool []turnStateCandidate, model string, now time.Time) []turnStateCandidate {
	// Newest-first order provides deterministic LRU-like model eviction. Expired
	// entries are removed before live buckets; total state is always bounded.
	out := make([]turnStateCandidate, 0, turnStateMaxModels*turnStatePoolSize)
	counts := map[string]int{model: 1}
	for _, v := range pool {
		if v.Model == "" || !now.Before(v.MintedAt.Add(turnStateTTL)) {
			continue
		}
		if counts[v.Model] == 0 && len(counts) >= turnStateMaxModels {
			continue
		}
		if counts[v.Model] >= turnStatePoolSize {
			continue
		}
		counts[v.Model]++
		out = append(out, v)
	}
	return out
}

func (s *OpenAIGatewayService) encodeTurnStatePool(pool turnStatePool, now time.Time) (map[string]any, error) {
	raw, err := json.Marshal(pool)
	if err != nil {
		return nil, err
	}
	ciphertext, err := s.turnStateEncryptor.Encrypt(string(raw))
	if err != nil {
		return nil, err
	}
	summary := []map[string]any{}
	for _, v := range pool.Candidates {
		fields := turnStateShapeFields(v.Blob)
		fields["model"] = v.Model
 fields["source"]=v.Source
		fields["minted_at"] = v.MintedAt.UTC().Format(time.RFC3339Nano)
		fields["expires_at"] = v.MintedAt.Add(turnStateTTL).UTC().Format(time.RFC3339Nano)
		fields["failed"] = v.Failed
		summary = append(summary, fields)
	}
	return map[string]any{CodexTurnStatePoolKey: ciphertext, CodexTurnStateSummaryKey: map[string]any{"owner": pool.Owner, "updated_at": now.UTC().Format(time.RFC3339Nano), "candidates": summary}}, nil
}

func (s *OpenAIGatewayService) rejectTurnStateCandidate(parent context.Context, a *turnStateAttempt) {
	if a.Injected == "" || !a.Rejected.CompareAndSwap(false, true) {
		return
	}
	s.logTurnState(a, "candidate_rejected", "invalid_encrypted_content_ambiguous", nil)
	s.turnStateSessions.set("rejected:"+a.Owner+":"+turnStateRejectionKey(a.Model, a.Injected), true, time.Now())
	store, ok := s.accountRepo.(CodexTurnStateStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), turnStateTimeout)
	defer cancel()
	err := store.MutateCodexTurnState(ctx, a.AccountID, func(latest *Account) (map[string]any, error) {
		if turnStateOwner(latest) != a.Owner {
			return nil, nil
		}
		pool, err := s.decodeTurnStatePool(latest)
		if err != nil {
			return nil, err
		}
		if pool.Rejected == nil {
			pool.Rejected = map[string]time.Time{}
		}
		now := time.Now()
		for key, expires := range pool.Rejected {
			if !now.Before(expires) {
				delete(pool.Rejected, key)
			}
		}
		if len(pool.Rejected) < 128 {
			pool.Rejected[turnStateRejectionKey(a.Model, a.Injected)] = now.Add(turnStateTTL)
		}
		for i := range pool.Candidates {
			if pool.Candidates[i].Model == a.Model && pool.Candidates[i].Blob == a.Injected {
				pool.Candidates[i].Failed = true
			}
		}
		if _, ok := pickTurnStateCandidate(pool, a.Model, time.Now()); !ok {
			s.logTurnState(a, "pool_exhausted", "continue_normal_scheduling", nil)
		}
		return s.encodeTurnStatePool(pool, time.Now())
	})
	if err != nil {
		s.logTurnState(a, "storage_error", "candidate_rejection_update_failed", nil)
	}
}

func turnStateShapeFields(value string) map[string]any {
	shape := "non_baseline"
	if openAITurnStateHealthy(value) {
		shape = "baseline"
	}
	envelope, ok := parseOpenAITurnStateEnvelope(value)
	return map[string]any{"length": len(strings.TrimSpace(value)), "blocks": envelope.CipherBlocks, "parsed": ok, "shape": shape}
}

// Public metadata only; stale identities must not appear as usable candidates.
func CodexTurnStatePublicExtra(a *Account) map[string]any {
	out := map[string]any{}
	if a == nil || a.Extra == nil {
		return nil
	}
	for k, v := range a.Extra {
		if k != CodexTurnStatePoolKey && k != "openai_turn_state_override" {
			out[k] = v
		}
	}
	owner := turnStateOwner(a)
	for _, key := range []string{CodexTurnStateObservationKey, CodexTurnStateSummaryKey, CodexTurnStateHuntKey} {
		record, _ := out[key].(map[string]any)
		if owner == "" || record["owner"] != owner {
			delete(out, key)
			continue
		}
		clone := make(map[string]any, len(record))
		for k, v := range record {
			if k != "owner" {
				clone[k] = v
			}
		}
		out[key] = clone
	}
	return out
}
