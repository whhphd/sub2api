//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetOpenAIUserAgentPrefersExtraOverCredentials(t *testing.T) {
	account := wireProfileTestAccount(true)
	account.Credentials["user_agent"] = "legacy-credential-ua"

	require.Equal(t, "legacy-credential-ua", account.GetOpenAIUserAgent(),
		"没有 extra 值时沿用历史的 credentials.user_agent")

	account.Extra[codexAccountUserAgentExtraKey] = "  "
	require.Equal(t, "legacy-credential-ua", account.GetOpenAIUserAgent(), "空白值不算配置")

	account.Extra[codexAccountUserAgentExtraKey] = "codex-tui/0.153.4 (Windows 10.0.26200; x86_64) WindowsTerminal (codex-tui; 0.153.4)"
	require.Equal(t,
		"codex-tui/0.153.4 (Windows 10.0.26200; x86_64) WindowsTerminal (codex-tui; 0.153.4)",
		account.GetOpenAIUserAgent(), "extra 优先")

	delete(account.Credentials, "user_agent")
	delete(account.Extra, codexAccountUserAgentExtraKey)
	require.Empty(t, account.GetOpenAIUserAgent(), "都没配置时由调用方回落到全局规范身份")

	other := wireProfileTestAccount(true)
	other.Platform = PlatformAnthropic
	other.Extra[codexAccountUserAgentExtraKey] = "should-not-leak"
	require.Empty(t, other.GetOpenAIUserAgent(), "非 OpenAI 账号不读该键")
}
