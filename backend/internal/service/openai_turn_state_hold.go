// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from KlN klno.13, 7f3855150586: optional missing-candidate hold.
// CallAI uses global policy and atomic, identity-checked hold transitions.
package service

import (
 "context"
 "fmt"
 "net/http"
 "strings"
 "time"
)

const OpenAITurnStateHoldReason GatewayFailureReason = "openai_turn_state_hold"
const openAITurnStateHoldReasonPrefix = "turn_state_hold:"

// A compare-and-swap must update the account and scheduler outbox atomically.
// It must not replace another fault, clear a concurrent fault, or mutate a new identity.
type CodexTurnStateHoldStore interface {
 CompareAndSwapTurnStateHold(context.Context, *Account, *time.Time, string) (bool, error)
}

func openAITurnStateHeldModel(a *Account, now time.Time) string {
 if a == nil || a.TempUnschedulableUntil == nil || !now.Before(*a.TempUnschedulableUntil) { return "" }
 model, ok := strings.CutPrefix(a.TempUnschedulableReason, openAITurnStateHoldReasonPrefix)
 if !ok { return "" }; return strings.TrimSpace(model)
}

func (s *OpenAIGatewayService) holdTurnStateIfUnfilled(req *http.Request, latest *Account, policy TurnStateHunterSettings) {
 a := turnStateAttemptFrom(req.Context())
 if a == nil || a.Probe || !policy.Enabled || !policy.HoldWhenDegraded || !s.hunterManagesModel(policy, a.AccountID, a.Model, time.Now()) { return }
 // A fresh baseline echoed by this account can still pass, as in the donor.
 // The cross-account echo guard already removed known foreign headers before binding.
 minted := openAITurnStateMintedAt(a.Original, time.Time{})
 rejected := s.turnStateSessions.needs("rejected:"+a.Owner+":"+turnStateRejectionKey(a.Model,a.Original),time.Now())
 pool, err := s.decodeTurnStatePool(latest)
 if err == nil { rejected = rejected || time.Now().Before(pool.Rejected[turnStateRejectionKey(a.Model,a.Original)]) }
 if !rejected && openAITurnStateHealthy(a.Original) && !minted.IsZero() && !minted.After(time.Now()) && time.Now().Before(minted.Add(turnStateTTL)) { return }
 a.HoldModel = a.Model
 store, ok := s.accountRepo.(CodexTurnStateHoldStore)
 if !ok { s.logTurnState(a,"hold_storage_error","store_unavailable",nil); return }
 now := time.Now()
 if openAITurnStateHeldModel(latest,now) != "" || !latest.IsSchedulable() { s.logTurnState(a,"hold","already_unavailable",nil); return }
 ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()),turnStateTimeout)
 defer cancel()
 until := now.Add(24*time.Hour)
 changed, err := store.CompareAndSwapTurnStateHold(ctx,latest,&until,openAITurnStateHoldReasonPrefix+a.Model)
 if err != nil { s.logTurnState(a,"hold_storage_error","persist_failed",nil); return }
 s.logTurnState(a,"hold","no_live_candidate",map[string]any{"persisted":changed})
}

func turnStateHoldError(req *http.Request) error {
 if req == nil { return nil }
 a := turnStateAttemptFrom(req.Context())
 if a == nil || a.HoldModel == "" { return nil }
 return &UpstreamFailoverError{StatusCode:http.StatusServiceUnavailable,Reason:OpenAITurnStateHoldReason,Scope:GatewayFailureScopeAccount,ClientStatusCode:http.StatusServiceUnavailable,ClientMessage:fmt.Sprintf("account has no usable x-codex-turn-state for %s; waiting for a baseline candidate",a.HoldModel)}
}

// Run even while the hunter is disabled and before budget/backoff gates. Re-read
// policy/account for every transition; a failed pool read must never release a hold.
func (s *OpenAITurnStateHunterService) syncHold(ctx context.Context, account *Account) {
 if account == nil || !strings.HasPrefix(account.TempUnschedulableReason,openAITurnStateHoldReasonPrefix) { return }
 store, ok := s.accounts.(CodexTurnStateHoldStore); if !ok { return }
 policy, err := s.gateway.hunterPolicy(ctx); if err != nil { return }
 latest, err := s.fresh(ctx,account.ID); if err != nil || latest == nil { return }
 model, ok := strings.CutPrefix(latest.TempUnschedulableReason,openAITurnStateHoldReasonPrefix)
 if !ok || model == "" { return }
 release := !policy.Enabled || !policy.HoldWhenDegraded || latest.Status != StatusActive || turnStateOwner(latest)=="" || (!policy.AutoModels && !policy.hunts(model)) || (policy.AutoModels && isOpenAIImageGenerationModel(model))
 if !release {
  pool, e := s.gateway.decodeTurnStatePool(latest); if e != nil { return }
  _, release = pickTurnStateCandidate(pool,model,s.now())
 }
 var until *time.Time
 reason := ""
 if !release {
  if latest.TempUnschedulableUntil != nil && latest.TempUnschedulableUntil.Sub(s.now()) >= 12*time.Hour { return }
  v := s.now().Add(24*time.Hour); until=&v;reason=openAITurnStateHoldReasonPrefix+model
 }
 op,cancel := context.WithTimeout(context.WithoutCancel(ctx),turnStateTimeout);defer cancel()
 changed,err := store.CompareAndSwapTurnStateHold(op,latest,until,reason)
 if err != nil { s.log(latest,model,"hunter_hold_error","transition_failed",nil); return }
 if changed { event:="hunter_hold_renewed";if release {event="hunter_hold_released"};s.log(latest,model,event,"policy_or_candidate",nil) }
}

// The settings save waits for this operation. Re-saving OFF retries cleanup even
// if an earlier save persisted the policy but failed to refresh Redis.
type TurnStateHoldReleaser interface {
 ReleaseTurnStateHoldsIfDisabled(context.Context) (int,error)
}
type TurnStateHoldReleaseResult struct {
 Released int `json:"released"`
 Complete bool `json:"complete"`
}
