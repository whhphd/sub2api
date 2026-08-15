package service

import (
	"context"
)

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
	account.Extra[codexFingerprintModeExtraKey] = string(codexFingerprintSession)
	return nil
}
