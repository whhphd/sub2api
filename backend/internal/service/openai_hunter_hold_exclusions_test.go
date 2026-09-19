package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestHunterHoldExemptionKeepsCollectionAndInjection(t *testing.T) {
	s, a, cfg := newHunterTest(t)
	ctx := context.Background()
	cfg.HoldWhenDegraded = true
	cfg.Models = []string{"gpt-5.6-terra", "gpt-test"}
	cfg.HoldExcludedModels = []string{"gpt-5.6-terra"}
	_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
	require.NoError(t, err)
	req := stateBoundRequest(s.gateway, a, 1, "terra", "gpt-5.6-terra")
	s.gateway.prepareTurnStateHTTP(req)
	require.NoError(t, turnStateHoldError(req))
	require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
	require.True(t, s.gateway.hunterManagesModel(cfg, a.ID, "gpt-5.6-terra", time.Now()))
	blob := turnStateFernetBlob(time.Now(), 12)
	s.gateway.recordTurnStateObservation(ctx, turnStateAttemptFrom(req.Context()), blob)
	next := stateBoundRequest(s.gateway, a, 1, "terra-next", "gpt-5.6-terra")
	s.gateway.prepareTurnStateHTTP(next)
	require.Equal(t, blob, next.Header.Get(openAICodexTurnStateHeader))
	other := stateBoundRequest(s.gateway, a, 1, "other", "gpt-test")
	s.gateway.prepareTurnStateHTTP(other)
	require.Error(t, turnStateHoldError(other))
	require.False(t, cfg.HoldExempt("gpt-5.6-terra-other"))
	copy := cfg.clone()
	copy.HoldExcludedModels[0] = "changed"
	require.True(t, cfg.HoldExempt("gpt-5.6-terra"))
	cfg.HoldExcludedModels = []string{"gpt-test", "gpt-test"}
	require.Error(t, cfg.validate())
}

func TestHunterTerraDefaultHonorsExplicitEmpty(t *testing.T){
 for _,raw:=range []string{"",`{"openai_oauth_turn_state_hunter":{"enabled":true}}`}{p,e:=parseOpenAIOAuthRuntimeSettings(raw);require.NoError(t,e);require.Equal(t,[]string{"gpt-5.6-terra"},p.TurnStateHunter.HoldExcludedModels)}
 p,e:=parseOpenAIOAuthRuntimeSettings(`{"openai_oauth_turn_state_hunter":{"hold_excluded_models":[]}}`);require.NoError(t,e);require.Empty(t,p.TurnStateHunter.HoldExcludedModels)
}
