package service

import (
	"context"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
)

// prepareCodexFingerprintAccount freezes the policy for one selected attempt.
// The scheduler/DB account and its saved fingerprint mode are never mutated.
func (s *OpenAIGatewayService) prepareCodexFingerprintAccount(ctx context.Context, account *Account) *Account {
	if account == nil || account.codexPolicyPrepared {
		return account
	}
	copy := snapshotOpenAIOutboundAccount(account)
	copy.codexPolicyPrepared = true
	copy.codexUniqueFingerprint = s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIAccountUniqueFingerprintEnabled
	enabled := false
	if s != nil && s.settingService != nil {
		enabled = s.settingService.GetOpenAIOAuthRuntimeSettings(ctx).CodexFingerprintEnhancementEnabled
	}
	copy.codexFingerprintEnhanced = enabled && copy.IsOpenAIOAuth()
	if copy.Extra != nil {
		delete(copy.Extra, codexFingerprintConvergenceExtraKey)
	}
	if copy.codexFingerprintEnhanced {
		if copy.Extra == nil {
			copy.Extra = make(map[string]any)
		}
		copy.Extra[codexFingerprintConvergenceExtraKey] = true
	}
	s.enrichCodexExitSnapshot(ctx, copy)
	return copy
}

func inheritCodexFingerprintPolicy(source, selected *Account) *Account {
	if source == nil || selected == nil || source == selected || !selected.codexPolicyPrepared {
		return source
	}
	copy := snapshotOpenAIOutboundAccount(source)
	copy.codexPolicyPrepared = selected.codexPolicyPrepared
	copy.codexFingerprintEnhanced = selected.codexFingerprintEnhanced && copy.IsOpenAIOAuth()
	copy.codexUniqueFingerprint = selected.codexUniqueFingerprint
	if copy.Extra != nil {
		delete(copy.Extra, codexFingerprintConvergenceExtraKey)
	}
	if copy.codexFingerprintEnhanced {
		if copy.Extra == nil {
			copy.Extra = make(map[string]any)
		}
		copy.Extra[codexFingerprintConvergenceExtraKey] = true
	}
	return copy
}

// The compact and image adapters may pass their selected account through more
// than one builder. Staging only updates the request-scoped credential source.
func stageCodexPolicySource(c *gin.Context, source *Account) {
	if c != nil {
		c.Set(codexAccountIdentitySourceContextKey, source)
	}
}

func activeCodexFingerprintMode(account *Account) codexFingerprintMode {
	if account == nil {
		return codexFingerprintOff
	}
	mode, _ := resolveCodexFingerprintMode(account, account.codexUniqueFingerprint)
	return mode
}

type openAIImagesWireTargetContextKey struct{}

func withOpenAIImagesWireTarget(ctx context.Context, target string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIImagesWireTargetContextKey{}, target)
}
func openAIImagesWireTarget(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	target, _ := ctx.Value(openAIImagesWireTargetContextKey{}).(string)
	return target
}

func finishCodexCompactIdentityFields(c *gin.Context, account *Account, body []byte) []byte {
	if c == nil || !c.GetBool("codex_compact_identity_pending") || codexDeviceWireProfileEnabled(c, account) {
		return body
	}
	for _, field := range []string{"prompt_cache_key", "access_programs"} {
		if next, err := sjson.DeleteBytes(body, field); err == nil {
			body = next
		}
	}
	return body
}

func codexEnhancementOnlyHeader(name string) bool {
	return name == "x-openai-memgen-request" || name == "x-responsesapi-include-timing-metrics"
}

// A live downstream WS cannot change its upstream handshake in place. Finish
// the active turn, then ask the client to reconnect before accepting a turn
// under a different policy or proxy. Existing defer paths release its lease.
func (s *OpenAIGatewayService) checkCodexWSAttemptConfiguration(ctx context.Context, account *Account) error {
	if s == nil || account == nil || !account.codexPolicyPrepared || !account.IsOpenAIOAuth() {
		return nil
	}
	changed := false
	if s.settingService != nil {
		current := s.settingService.GetOpenAIOAuthRuntimeSettings(ctx)
		changed = current.CodexFingerprintEnhancementEnabled != account.codexFingerprintEnhanced
	}
	if !changed && s.schedulerSnapshot != nil {
		latest, err := s.schedulerSnapshot.GetAccount(ctx, account.ID)
		if err == nil && latest != nil {
			changed = codexWireTimezoneProxyTag(latest) != codexWireTimezoneProxyTag(account)
		}
	}
	if changed {
		return NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "upstream configuration changed; reconnect to continue", nil)
	}
	return nil
}
