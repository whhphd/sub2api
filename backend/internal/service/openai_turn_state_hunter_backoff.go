package service

import (
	"context"
	"github.com/tidwall/gjson"
	"net/http"
	"strconv"
	"time"
)

// Fixed labels only: neither error bodies nor arbitrary provider codes are persisted.
func classifyHunterError(a *Account, status int, h http.Header, body []byte, now time.Time) (string, string, time.Time) {
	if isOpenAICloudflareForbiddenResponse(a, status, h, body) || h.Get("cf-mitigated") == "challenge" {
		return "exit_challenge", "proxy", now.Add(time.Minute)
	}
	if status == http.StatusProxyAuthRequired {
		return "proxy_authentication", "proxy", now.Add(5 * time.Minute)
	}
	if status == http.StatusUnauthorized {
		return "credential_rejected", "account", now.Add(time.Hour)
	}
	if status == http.StatusForbidden && openAIStreamCredentialAuthFailure(body) {
		return "credential_rejected", "account", now.Add(time.Hour)
	}
	code := gjson.GetBytes(body, "error.code").String()
	if code == "" {
		code = gjson.GetBytes(body, "response.error.code").String()
	}
	if code == "model_not_found" || code == "model_not_supported" || code == "unsupported_model" {
		return "model_unavailable", "model", now.Add(15 * time.Minute)
	}
	if status == http.StatusForbidden {
		return "forbidden_unclassified", "model", now.Add(time.Minute)
	}
	if status == http.StatusTooManyRequests {
		disposition, reset := classifyOpenAIOAuth429(h, body)
		if disposition != openAIOAuth429Transient || code == "usage_limit_reached" || code == "insufficient_quota" || code == "quota_exceeded" {
			until := now.Add(time.Hour)
			if reset != nil && reset.After(now) {
				until = *reset
			}
			return "quota_exhausted", "account", until
		}
		return "rate_limited_transient", "model", now.Add(time.Minute)
	}
	return diagnosticErrorClass(status, body), "model", now.Add(15 * time.Minute)
}

// Legacy status-only 403 cooldowns do not establish an account-wide fault.
// Migrate once before spending. Preserve old 401/429 waits: old logs cannot prove quota scope.
func (st *openAITurnStateHuntState) upgradeBackoff(now time.Time) {
	if st.BackoffVersion >= 2 {
		return
	}
	st.BackoffVersion = 2
	if st.CapWait || len(st.Last) == 0 || !now.Before(st.NextAt) {
		return
	}
	last := st.Last[0]
	if last.Status == 403 || last.Status >= 500 {
		if st.ModelNext == nil {
			st.ModelNext = map[string]time.Time{}
		}
		until := last.At.Add(time.Minute)
		if until.After(now) {
			st.ModelNext[last.Model] = until
		}
		st.NextAt = time.Time{}
	}
}
func (st *openAITurnStateHuntState) applyBackoff(result *openAITurnStateHuntAttempt, now time.Time) {
	if result.Status == 200 && result.Error == "" {
		return
	}
	if result.BackoffScope == "" {
		if result.transport {
			result.Error = "transport_error"
			result.BackoffScope = "proxy"
			result.BackoffUntil = now.Add(time.Minute)
		} else {
			if result.Status == 0 || result.Status == http.StatusOK {result.BackoffScope="model";result.BackoffUntil=now.Add(15*time.Minute)}else{result.Error, result.BackoffScope, result.BackoffUntil = classifyHunterError(nil, result.Status, nil, nil, now)}
		}
	}
	switch result.BackoffScope {
	case "account":
		st.NextAt = result.BackoffUntil
	case "proxy":
		if st.ProxyNext == nil {
			st.ProxyNext = map[string]time.Time{}
		}
		boundedHunterCooldown(st.ProxyNext,strconv.FormatInt(result.ProxyID,10),result.BackoffUntil,64)
	default:
		if st.ModelNext == nil {
			st.ModelNext = map[string]time.Time{}
		}
		boundedHunterCooldown(st.ModelNext,result.Model,result.BackoffUntil,16)
	}
	for k, v := range st.ModelNext {
		if !now.Before(v) {
			delete(st.ModelNext, k)
		}
	}
	for k, v := range st.ProxyNext {
		if !now.Before(v) {
			delete(st.ProxyNext, k)
		}
	}
}
func (s *OpenAITurnStateHunterService) hunterAccountBlocked(ctx context.Context, a *Account, now time.Time) bool {
	if a == nil || a.Status != StatusActive || !a.Schedulable {
		return true
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
		return true
	}
	if a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt) {
		return true
	}
	if a.OverloadUntil != nil && now.Before(*a.OverloadUntil) {
		return true
	}
	if a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil) && openAITurnStateHeldModel(a, now) == "" {
		return true
	}
	if paused, _ := shouldAutoPauseOpenAIAccountByQuota(s.gateway.withOpenAIQuotaAutoPauseContext(ctx), a); paused {
		return true
	}
	// Explicit full windows still skip even when auto-pause thresholds are disabled.
	for _, window := range []string{"5h", "7d"} {
		if usage, ok := resolveOpenAIQuotaUtilization(a.Extra, window, now); ok && usage >= 1 {
			return true
		}
	}
	return false
}

func boundedHunterCooldown(entries map[string]time.Time,key string,until time.Time,limit int){
 if _,exists:=entries[key];!exists&&len(entries)>=limit{oldest:="";var at time.Time;for k,v:=range entries{if oldest==""||v.Before(at){oldest=k;at=v}};delete(entries,oldest)}
 entries[key]=until
}
