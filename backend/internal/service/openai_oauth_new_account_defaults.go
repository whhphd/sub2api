package service

import (
	"context"
)

// ApplyOpenAIOAuthNewAccountDefaults is retained as a compatibility hook for
// all account creation/import paths. Runtime behavior is now global, so new
// accounts must not receive account-level injection or 429 policy fields.
func (s *SettingService) ApplyOpenAIOAuthNewAccountDefaults(context.Context, *Account) error {
	return nil
}
