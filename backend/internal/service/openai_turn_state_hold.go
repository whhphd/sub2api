package service

import (
	"fmt"
	"net/http"
	"strings"
)

// OpenAITurnStateHoldReason identifies an optional local scheduling hold caused
// by a hunted model having no usable candidate.
const OpenAITurnStateHoldReason GatewayFailureReason = "openai_turn_state_hold"

func turnStateHoldError(req *http.Request) error {
	if req == nil { return nil }
	attempt := turnStateAttemptFrom(req.Context())
	if attempt == nil || strings.TrimSpace(attempt.HoldModel) == "" { return nil }
	return &UpstreamFailoverError{
		StatusCode: http.StatusServiceUnavailable,
		Reason: OpenAITurnStateHoldReason,
		Scope: GatewayFailureScopeAccount,
		ClientStatusCode: http.StatusServiceUnavailable,
		ClientMessage: fmt.Sprintf("account has no healthy x-codex-turn-state for %s and is paused until the hunter finds one", attempt.HoldModel),
	}
}
