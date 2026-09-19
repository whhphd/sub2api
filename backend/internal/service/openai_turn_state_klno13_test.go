// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from KlN klno.13 (7f3855150586), with CallAI global policy/CAS boundaries.
package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func (r *hunterAccounts) CompareAndSwapTurnStateHold(_ context.Context, e *Account, u *time.Time, reason string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return false, errors.New("offline")
	}
	a := r.account
	if !reflect.DeepEqual(a.Credentials, e.Credentials) || a.TempUnschedulableReason != e.TempUnschedulableReason || !sameHoldTime(a.TempUnschedulableUntil, e.TempUnschedulableUntil) {
		return false, nil
	}
	a.TempUnschedulableUntil = u
	a.TempUnschedulableReason = reason
	return true, nil
}
func TestHunterHoldLifecycle(t *testing.T) {
	s, a, cfg := newHunterTest(t)
	ctx := context.Background()
	cfg.HoldWhenDegraded = true
	_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
	require.NoError(t, err)
	req := stateBoundRequest(s.gateway, a, 1, "hold", "gpt-test")
	s.gateway.prepareTurnStateHTTP(req)
	var fe *UpstreamFailoverError
	require.ErrorAs(t, turnStateHoldError(req), &fe)
	require.Equal(t, 503, fe.StatusCode)
	require.False(t, fe.ShouldReportAccountScheduleFailure())
	require.Same(t, fe, s.gateway.handleOpenAIUpstreamTransportError(ctx, nil, a, fe, false))
	latest, err := s.fresh(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "gpt-test", openAITurnStateHeldModel(latest, time.Now()))
	cfg.HoldWhenDegraded = false
	_, err = s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
	require.NoError(t, err)
	s.syncHold(ctx, latest)
	latest, err = s.fresh(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, latest.TempUnschedulableReason)
}
func TestHunterAutoModelsNaturalOnlyAndDisabled(t *testing.T) {
	s, a, cfg := newHunterTest(t)
	ctx := context.Background()
	cfg.AutoModels = true
	cfg.Models = nil
	_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
	require.NoError(t, err)
	req := stateBoundRequest(s.gateway, a, 1, "natural", "gpt-other")
	s.gateway.prepareTurnStateHTTP(req)
	s.gateway.recordTurnStateObservation(ctx, turnStateAttemptFrom(req.Context()), turnStateFernetBlob(time.Now(), 13))
	require.Equal(t, []string{"gpt-other"}, s.gateway.hunterModelsForAccount(cfg, a.ID, time.Now()))
	s.gateway.turnStateTraffic.note(a.ID, "gpt-image-2", time.Now())
	s.gateway.turnStateTraffic.noteMinted(a.ID, "gpt-image-2", time.Now())
	require.Equal(t, []string{"gpt-other"}, s.gateway.hunterModelsForAccount(cfg, a.ID, time.Now()))
	cfg.Enabled = false
	require.False(t, s.gateway.hunterManagesModel(cfg, a.ID, "gpt-other", time.Now()))
}

type hunterKeys struct {
	APIKeyQuotaUpdater
	key *APIKey
}

func (k hunterKeys) GetByID(context.Context, int64) (*APIKey, error) { return k.key, nil }
func TestHunterUsageBillingAndDisabled(t *testing.T) {
	s, a, cfg := newHunterTest(t)
	cfg.UsageAPIKeyID = 7
	cfg.UsageAccountingEnabled = true
	s.apiKeys = hunterKeys{key: &APIKey{ID: 7, Status: StatusAPIKeyActive, User: &User{ID: 1, Status: StatusActive}}}
	calls := 0
	s.recordUsage = func(_ context.Context, in *OpenAIRecordUsageInput) error {
		calls++
		require.Equal(t, RequestTypeTurnStateProbe, in.RequestType)
		require.Greater(t, in.Result.Usage.InputTokens, 0)
		require.Zero(t, in.Result.Usage.OutputTokens)
		return nil
	}
	req, _ := http.NewRequest(http.MethodPost, "https://example.invalid/responses", nil)
	s.recordProbeUsage(context.Background(), a, "gpt-5.5", cfg, req, http.Header{}, openAITurnStateHuntAttempt{Status: 200})
	require.Equal(t, 1, calls)
	cfg.UsageAccountingEnabled = false
	s.recordProbeUsage(context.Background(), a, "gpt-5.5", cfg, req, http.Header{}, openAITurnStateHuntAttempt{Status: 200})
	require.Equal(t, 1, calls)
	require.Equal(t, "probe", RequestTypeTurnStateProbe.String())
}
func TestHunterConfigCompatibilityAndValidation(t *testing.T) {
	cfg := DefaultTurnStateHunterSettings()
	require.True(t, cfg.UsageAccountingEnabled)
	require.False(t, cfg.HoldWhenDegraded)
	old, err := parseOpenAIOAuthRuntimeSettings(`{"openai_oauth_turn_state_hunter":{"enabled":true,"models":["gpt-test"],"proxy_ids":[197],"max_per_hour":6000,"per_account_max_per_hour":500,"gap_seconds":5,"lead_minutes":10,"retry_minutes":10,"idle_minutes":-1,"reasoning_effort":"high"},"openai_oauth_turn_state_auto_enabled":true}`)
	require.NoError(t, err)
	require.True(t, old.TurnStateHunter.UsageAccountingEnabled)
	cfg.Enabled = true
	cfg.AutoModels = true
	cfg.ProxyIDs = []int64{197}
	cfg.RotatingProxyIDs = []int64{197}
	require.NoError(t, cfg.validate())
	for _, ids := range [][]int64{{1}, {197, 197}, {-1}} {
		cfg.RotatingProxyIDs = ids
		require.Error(t, cfg.validate(), fmt.Sprint(ids))
	}
}

func TestHunterHoldOnlyMatchingLiveCandidateReleases(t *testing.T) {
	for _, kind := range []string{"wrong", "expired", "failed", "valid", "other_fault", "disabled", "auto_off"} {
		t.Run(kind, func(t *testing.T) {
			s, a, cfg := newHunterTest(t)
			ctx := context.Background()
			cfg.HoldWhenDegraded = true
			_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
			require.NoError(t, err)
			req := stateBoundRequest(s.gateway, a, 1, "hold", "gpt-test")
			s.gateway.prepareTurnStateHTTP(req)
			require.Error(t, turnStateHoldError(req))
			held, err := s.fresh(ctx, a.ID)
			require.NoError(t, err)
			c := turnStateCandidate{Model: "gpt-test", Blob: turnStateFernetBlob(time.Now(), 12), MintedAt: time.Now()}
			switch kind {
			case "wrong":
				c.Model = "other"
			case "expired":
				c.MintedAt = time.Now().Add(-2 * time.Hour)
			case "failed":
				c.Failed = true
			}
			encoded, err := s.gateway.encodeTurnStatePool(turnStatePool{Owner: turnStateOwner(a), Candidates: []turnStateCandidate{c}}, time.Now())
			require.NoError(t, err)
			repo := s.accounts.(*hunterAccounts)
			require.NoError(t, repo.MutateCodexTurnState(ctx, a.ID, func(*Account) (map[string]any, error) { return encoded, nil }))
			switch kind {
			case "other_fault":
				repo.account.TempUnschedulableReason = "credential_rejected"
			case "disabled":
				cfg.Enabled = false
				_, err = s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
				require.NoError(t, err)
			case "auto_off":
				off := false
				_, err = s.gateway.settingService.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, nil, &off)
				require.NoError(t, err)
			}
			s.syncHold(ctx, held)
			latest, err := s.fresh(ctx, a.ID)
			require.NoError(t, err)
			switch kind {
			case "valid", "disabled", "auto_off":
				require.Empty(t, latest.TempUnschedulableReason)
			case "other_fault":
				require.Equal(t, "credential_rejected", latest.TempUnschedulableReason)
			default:
				require.Equal(t, "gpt-test", openAITurnStateHeldModel(latest, time.Now()))
			}
		})
	}
}
func TestHunterRefundNeverDebitsNewWindow(t *testing.T) {
	s, _, cfg := newHunterTest(t)
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	var old time.Time
	ok, err := s.reserveGlobal(ctx, cfg, &old)
	require.NoError(t, err)
	require.True(t, ok)
	now = now.Add(2 * time.Hour)
	cfg.MaxPerHour = 1
	ok, err = s.reserveGlobal(ctx, cfg)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.releaseGlobal(ctx, old))
	ok, err = s.reserveGlobal(ctx, cfg)
	require.NoError(t, err)
	require.False(t, ok)
}

type immediateHoldRelease struct {
	calls  int
	fail   bool
	policy func() bool
}

func (r *immediateHoldRelease) ReleaseTurnStateHoldsIfDisabled(context.Context) (int, error) {
	r.calls++
	if r.policy() {
		return 0, errors.New("still on")
	}
	if r.fail {
		return 0, errors.New("redis offline")
	}
	return 22, nil
}
func TestHunterSavingOffSynchronouslyReleasesAndRetries(t *testing.T) {
	s, _, cfg := newHunterTest(t)
	ctx := context.Background()
	cfg.HoldWhenDegraded = true
	_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
	require.NoError(t, err)
	r := &immediateHoldRelease{policy: func() bool { p, e := s.gateway.hunterPolicy(ctx); return e != nil || p.HoldWhenDegraded }}
	s.gateway.settingService.turnStateHoldReleaser = r
	cfg.HoldWhenDegraded = false
	r.fail = true
	_, err = s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
	require.ErrorContains(t, err, "settings saved")
	require.Equal(t, 1, r.calls)
	r.fail = false
	got, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 22, got.TurnStateHoldRelease.Released)
	require.True(t, got.TurnStateHoldRelease.Complete)
	require.Equal(t, 2, r.calls)
}

func sameHoldTime(a,b *time.Time)bool {if a==nil || b==nil {return a==nil && b==nil};return a.Equal(*b)}
