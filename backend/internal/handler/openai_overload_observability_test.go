package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestOpenAIOverloadLogStateRetryRecoveryLifecycle(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	reqLog := zap.New(core)
	account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	diagnostics := &service.OpenAIOverloadStreamDiagnostics{
		LastEventType:         "response.output_item.added",
		EventCount:            3,
		StreamStage:           "output_started",
		SemanticOutputStarted: false,
		DownstreamCommitted:   false,
		UpstreamResponseID:    "resp_observed",
	}
	err := &service.UpstreamFailoverError{RequestScopedTransient: true, OverloadDiagnostics: diagnostics}
	state := &openAIOverloadLogState{enabled: true}

	require.True(t, state.observe(reqLog, err, account, "gpt-test", "/v1/responses", true, 1))
	state.retry(reqLog, account, "gpt-test", "/v1/responses", true, 1, "next_account")
	state.recovered(reqLog, &service.Account{ID: 43, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}, "gpt-test", "/v1/responses", true, 2)

	entries := logs.All()
	require.Len(t, entries, 3)
	require.Equal(t, "openai.oauth.overload_detected", entries[0].Message)
	require.Equal(t, "openai.oauth.overload_retried", entries[1].Message)
	require.Equal(t, "openai.oauth.overload_recovered", entries[2].Message)
	require.Equal(t, "output_started", entries[0].ContextMap()["stream_stage"])
	require.Equal(t, "response.output_item.added", entries[0].ContextMap()["last_event_type"])
	require.Equal(t, "next_account", entries[1].ContextMap()["retry_scope"])
	require.Equal(t, int64(1), entries[2].ContextMap()["detected_count"])
}

func TestOpenAIOverloadLogStateExposedAndDisabled(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	reqLog := zap.New(core)
	account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	diagnostics := &service.OpenAIOverloadStreamDiagnostics{
		LastEventType:         "response.output_text.delta",
		EventCount:            5,
		StreamStage:           "semantic_output",
		SemanticOutputStarted: true,
		SemanticOutputKind:    "text",
		DownstreamCommitted:   true,
	}
	err := &service.OpenAIOverloadExposedError{Message: "Our servers are currently overloaded. Please try again later.", Diagnostics: *diagnostics}

	disabled := &openAIOverloadLogState{}
	require.False(t, disabled.observe(reqLog, err, account, "gpt-test", "/v1/messages", true, 1))
	require.Empty(t, logs.All())

	enabled := &openAIOverloadLogState{enabled: true}
	require.True(t, enabled.observe(reqLog, err, account, "gpt-test", "/v1/messages", true, 1))
	enabled.exposed(reqLog, account, "gpt-test", "/v1/messages", true, 1, "already_written")

	entries := logs.All()
	require.Len(t, entries, 2)
	require.Equal(t, "openai.oauth.overload_detected", entries[0].Message)
	require.Equal(t, "openai.oauth.overload_exposed", entries[1].Message)
	require.Equal(t, "semantic_output", entries[1].ContextMap()["stream_stage"])
	require.Equal(t, "text", entries[1].ContextMap()["semantic_output_kind"])
	require.Equal(t, "already_written", entries[1].ContextMap()["exposure_mode"])
}
