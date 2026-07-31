package service

import (
	"context"
	"errors"
	"fmt"
)

// ApplyOpenAIOAuthNewAccountDefaults applies creation-only defaults controlled by system settings.
// Later account updates remain free to disable either option independently.
func (s *SettingService) ApplyOpenAIOAuthNewAccountDefaults(ctx context.Context, account *Account) error {
	if account == nil || !account.IsOpenAIOAuth() || s == nil || s.settingRepo == nil {
		return nil
	}

	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIOAuthNewAccountNoopToolcallDefaultsEnabled)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return nil
		}
		return fmt.Errorf("get OpenAI OAuth new-account defaults setting: %w", err)
	}
	if value != "true" {
		return nil
	}

	if account.Extra == nil {
		account.Extra = make(map[string]any, 2)
	}
	account.Extra[openAIOAuthInjectNoopToolCallExtraKey] = true
	account.Extra[openAIOAuthInjectNoopToolCallIgnore429CooldownExtraKey] = true
	return nil
}
