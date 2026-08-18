package service

import (
	"context"
	"strings"
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

// ApplyOpenAIOAuthNewAccountDefaults is called by every account creation/import
// path. It only fills an absent fingerprint mode; an explicit account value is
// preserved, including an explicit "off" value.
func (s *SettingService) ApplyOpenAIOAuthNewAccountDefaults(ctx context.Context, account *Account) error {
	if account == nil || !account.IsOpenAIOAuth() {
		return nil
	}
	if account.Extra != nil {
		if _, exists := account.Extra[codexFingerprintModeExtraKey]; exists {
			return nil
		}
	}
	if !s.IsOpenAIOAuthDefaultCodexFingerprintEnabled(ctx) {
		return nil
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[codexFingerprintModeExtraKey] = s.OpenAIOAuthDefaultCodexFingerprintMode(ctx)
	return nil
}
