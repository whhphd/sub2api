package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
)

const defaultOpenAIOAuthCodexFingerprintMode = string(codexFingerprintSession)

// NormalizeOpenAIOAuthDefaultCodexFingerprintMode keeps the persisted global
// setting aligned with the four modes supported by the account-level logic.
func NormalizeOpenAIOAuthDefaultCodexFingerprintMode(value string) string {
	switch codexFingerprintMode(strings.TrimSpace(value)) {
	case codexFingerprintOff, codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull:
		return strings.TrimSpace(value)
	default:
		return defaultOpenAIOAuthCodexFingerprintMode
	}
}

// IsOpenAIOAuthDefaultCodexFingerprintEnabled reports the system default for
// newly-created OpenAI OAuth accounts. Missing/invalid settings intentionally
// default to enabled so an upgrade preserves the prior behavior for new users.
func (s *SettingService) IsOpenAIOAuthDefaultCodexFingerprintEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIOAuthDefaultCodexFingerprintEnabled)
	if err != nil {
		return true
	}
	return value != "false"
}

// OpenAIOAuthDefaultCodexFingerprintMode returns the normalized mode for new
// OpenAI OAuth accounts. Missing or invalid values preserve the historical
// device+session default.
func (s *SettingService) OpenAIOAuthDefaultCodexFingerprintMode(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIOAuthDefaultCodexFingerprintMode)
	if err != nil {
		return defaultOpenAIOAuthCodexFingerprintMode
	}
	return NormalizeOpenAIOAuthDefaultCodexFingerprintMode(value)
}

// NormalizeOpenAIOAuthNewAccountProxyPoolIDs removes invalid and duplicate IDs
// and returns a stable representation for persistence and comparison.
func NormalizeOpenAIOAuthNewAccountProxyPoolIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized
}

// ParseOpenAIOAuthNewAccountProxyPoolIDs decodes the persisted JSON list.
// Malformed values fail closed to an empty pool.
func ParseOpenAIOAuthNewAccountProxyPoolIDs(raw string) []int64 {
	var ids []int64
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &ids); err != nil {
		return []int64{}
	}
	return NormalizeOpenAIOAuthNewAccountProxyPoolIDs(ids)
}

func (s *SettingService) applyOpenAIOAuthNewAccountProxyDefault(ctx context.Context, account *Account) {
	if account.ProxyID != nil || s.proxyRepo == nil {
		return
	}
	settings, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyOpenAIOAuthNewAccountProxyPoolEnabled,
		SettingKeyOpenAIOAuthNewAccountProxyPoolIDs,
	})
	if err != nil || settings[SettingKeyOpenAIOAuthNewAccountProxyPoolEnabled] != "true" {
		return
	}
	configuredIDs := ParseOpenAIOAuthNewAccountProxyPoolIDs(
		settings[SettingKeyOpenAIOAuthNewAccountProxyPoolIDs],
	)
	if len(configuredIDs) == 0 {
		return
	}

	proxies, err := s.proxyRepo.ListByIDs(ctx, configuredIDs)
	if err != nil {
		slog.WarnContext(ctx, "openai_oauth_new_account_proxy_pool_lookup_failed", "error", err)
		return
	}
	now := time.Now()
	validIDs := make([]int64, 0, len(proxies))
	for i := range proxies {
		if proxies[i].IsActive() && !proxies[i].IsExpired(now) {
			validIDs = append(validIDs, proxies[i].ID)
		}
	}
	if len(validIDs) == 0 {
		slog.WarnContext(ctx, "openai_oauth_new_account_proxy_pool_has_no_active_candidates")
		return
	}
	selectedID := validIDs[rand.IntN(len(validIDs))]
	account.ProxyID = &selectedID
}

// ApplyOpenAIOAuthNewAccountDefaults is called by every account creation/import
// path. It only fills an absent fingerprint mode; an explicit account value is
// preserved, including an explicit "off" value.
func (s *SettingService) ApplyOpenAIOAuthNewAccountDefaults(ctx context.Context, account *Account) error {
	if account == nil || !account.IsOpenAIOAuth() {
		return nil
	}
	_, hasExplicitFingerprintMode := account.Extra[codexFingerprintModeExtraKey]
	if !hasExplicitFingerprintMode && s.IsOpenAIOAuthDefaultCodexFingerprintEnabled(ctx) {
		if account.Extra == nil {
			account.Extra = make(map[string]any)
		}
		account.Extra[codexFingerprintModeExtraKey] = s.OpenAIOAuthDefaultCodexFingerprintMode(ctx)
	}
	s.applyOpenAIOAuthNewAccountProxyDefault(ctx, account)
	return nil
}
