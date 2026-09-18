// SPDX-License-Identifier: LGPL-3.0-only
// Cases adapted from KlN klno.12 openai_turn_state_hunter_test.go; CallAI global-policy harness.
package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

type hunterAccounts struct {
	*stateTestRepo
	onMutate func()
}

func (r *hunterAccounts) MutateCodexTurnState(ctx context.Context, id int64, fn func(*Account) (map[string]any, error)) error {
	if r.onMutate != nil {
		r.onMutate()
	}
	return r.stateTestRepo.MutateCodexTurnState(ctx, id, fn)
}
func (r *hunterAccounts) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.ReadCodexTurnState(ctx, id)
}
func (r *hunterAccounts) ListByPlatform(ctx context.Context, _ string) ([]Account, error) {
	a, e := r.ReadCodexTurnState(ctx, r.account.ID)
	if e != nil {
		return nil, e
	}
	return []Account{*a}, nil
}

type hunterProxies struct {
	ProxyRepository
	values []Proxy
}

func (r *hunterProxies) ListByIDs(context.Context, []int64) ([]Proxy, error) { return r.values, nil }
func newHunterTest(t *testing.T) (*OpenAITurnStateHunterService, *Account, TurnStateHunterSettings) {
	t.Helper()
	g, r, _, a, _ := stateAutoTest(t)
	a.Status = StatusActive
	a.Concurrency = 1
	a.Credentials["access_token"] = "offline-access"
	r.account = cloneStateAccount(a)
	repo := &hunterAccounts{stateTestRepo: r}
	g.accountRepo = repo
	cfg := DefaultTurnStateHunterSettings()
	cfg.Enabled = true
	cfg.Models = []string{"gpt-test"}
	cfg.ProxyIDs = []int64{1}
	cfg.IdleMinutes = -1
	_, err := g.settingService.UpdateOpenAIOAuthRuntimePolicy(context.Background(), &cfg, nil, nil)
	require.NoError(t, err)
	p := Proxy{ID: 1, Host: "us.1024proxy.io", Username: "test-region-US", Password: "secret", Port: 3000, Protocol: "socks5", Status: StatusActive}
	s := NewOpenAITurnStateHunterService(g, repo, &hunterProxies{values: []Proxy{p}}, nil, &fakeLeaderLockCache{})
	s.probeOverride = func(_ context.Context, _ *Account, m string, _ TurnStateHunterSettings, p Proxy) openAITurnStateHuntAttempt {
		return openAITurnStateHuntAttempt{At: s.now(), Model: m, ProxyID: p.ID, Status: 429, Error: "rate_limited"}
	}
	return s, a, cfg
}
func TestHunterGlobalSettingsAtomicAndClone(t *testing.T) {
	s, _, cfg := newHunterTest(t)
	ctx := context.Background()
	p, e := s.gateway.hunterPolicy(ctx)
	require.NoError(t, e)
	require.True(t, p.Enabled)
	p.Models[0] = "changed"
	again, e := s.gateway.hunterPolicy(ctx)
	require.NoError(t, e)
	require.Equal(t, "gpt-test", again.Models[0])
	bad := cfg
	bad.ReasoningEffort = "bogus"
	off := false
	_, e = s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &bad, nil, nil, nil, nil, nil, nil, nil, &off)
	require.Error(t, e)
	require.True(t, s.gateway.turnStateAutoEnabled(ctx), "invalid hunter PATCH must not partially turn takeover off")
	_, e = s.gateway.settingService.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, nil, &off)
	require.NoError(t, e)
	p, e = s.gateway.hunterPolicy(ctx)
	require.NoError(t, e)
	require.False(t, p.Enabled)
}
func TestHunterGlobalBudgetSurvivesRestartAndConcurrency(t *testing.T) {
	s, _, cfg := newHunterTest(t)
	cfg.MaxPerHour = 2
	var wg sync.WaitGroup
	results := make(chan bool, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, e := s.reserveGlobal(context.Background(), cfg)
			if e == nil {
				results <- ok
			}
		}()
	}
	wg.Wait()
	close(results)
	count := 0
	for ok := range results {
		if ok {
			count++
		}
	}
	require.Equal(t, 2, count)
	peer := NewOpenAITurnStateHunterService(s.gateway, s.accounts, s.proxies, nil, s.leader)
	peer.owner = "peer"
	ok, e := peer.reserveGlobal(context.Background(), cfg)
	require.NoError(t, e)
	require.False(t, ok)
	peer.now = func() time.Time { return time.Now().Add(61 * time.Minute) }
	ok, e = peer.reserveGlobal(context.Background(), cfg)
	require.NoError(t, e)
	require.True(t, ok)
}
func TestHunterIdleFreshCapAndReadFailure(t *testing.T) {
	s, a, cfg := newHunterTest(t)
	ctx := context.Background()
	cfg.IdleMinutes = 60
	_, e := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
	require.NoError(t, e)
	spent, halt := s.huntOne(ctx, a, cfg)
	require.False(t, spent)
	require.False(t, halt)
	latest, e := s.fresh(ctx, a.ID)
	require.NoError(t, e)
	require.Equal(t, "idle", readOpenAITurnStateHuntState(latest).Gate)
	s.gateway.turnStateTraffic.note(a.ID, "gpt-test", s.now())
	spent, halt = s.huntOne(ctx, a, cfg)
	require.True(t, spent)
	require.False(t, halt)
	latest, _ = s.fresh(ctx, a.ID)
	st := readOpenAITurnStateHuntState(latest)
	require.Equal(t, 1, st.HourCount)
	require.True(t, st.NextAt.After(s.now().Add(59*time.Minute)))
	require.True(t, latest.Schedulable, "probe failure must not change scheduling")
	s.accounts.(*hunterAccounts).fail = true
	spent, halt = s.huntOne(ctx, a, cfg)
	require.False(t, spent)
	require.False(t, halt)
}
func TestHunterFirstTurnInjectionAndNoCrossModel(t *testing.T) {
	s, a, _ := newHunterTest(t)
	g := s.gateway
	ctx := context.Background()
	seed := stateBoundRequest(g, a, 1, "seed", "gpt-test")
	g.prepareTurnStateHTTP(seed)
	blob := turnStateFernetBlob(time.Now(), 12)
	g.recordTurnStateObservation(ctx, turnStateAttemptFrom(seed.Context()), blob)
	next := stateBoundRequest(g, a, 2, "new-client", "gpt-test")
	g.prepareTurnStateHTTP(next)
	require.Equal(t, blob, next.Header.Get(openAICodexTurnStateHeader))
	other := stateBoundRequest(g, a, 2, "new-client", "other-model")
	g.prepareTurnStateHTTP(other)
	require.Empty(t, other.Header.Get(openAICodexTurnStateHeader))
}
func TestHunterProviderRotationAndBackoff(t *testing.T) {
	for _, tc := range []struct {
		host, user string
		want       bool
	}{
		{"p.webshare.io", "user-us-rotate", true}, {"p.webshare.io", "user-us-1", false}, {"evilwebshare.io", "user-rotate", false},
		{"us.1024proxy.io", "user-region-US", true}, {"us.1024proxy.io", "user-region-US-sid-example-t-5", false}, {"1024proxy.io.evil.test", "user", false}, {"other.invalid", "user-rotate", false},
	} {
		require.Equal(t, tc.want, openAITurnStateHuntProxyRotating(Proxy{Host: tc.host, Username: tc.user}))
	}
	require.Equal(t, time.Hour, openAITurnStateHuntBackoff(429))
	require.Equal(t, 6*time.Hour, openAITurnStateHuntBackoff(401))
	now := time.Now()
	st := openAITurnStateHuntState{NextAt: now.Add(time.Hour), CapWait: true, HourCount: 30}
	cfg := DefaultTurnStateHunterSettings()
	require.True(t, st.waiting(cfg, now))
	cfg.PerAccountMaxPerHour = 40
	require.False(t, st.waiting(cfg, now))
	st.recordExit(openAITurnStateHuntExit{IP: "192.0.2.1", At: now, Healthy: false})
	require.True(t, st.exitCoolingDown("192.0.2.1", now))
	require.False(t, st.exitCoolingDown("192.0.2.1", now.Add(8*24*time.Hour)))
}

// Shared transport mock retains the original request and close-before-persist evidence.
type hunterBody struct {
	closed bool
	reads  int
}

func (b *hunterBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (b *hunterBody) Close() error             { b.closed = true; return nil }

type hunterUpstream struct {
	HTTPUpstream
	req      *http.Request
	response *http.Response
}

func (u *hunterUpstream) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.req = r
	return u.response, nil
}
func TestHunterProbeWireAndEncryptedPool(t *testing.T) {
	s, a, cfg := newHunterTest(t)
	g := s.gateway
	g.toolCorrector = NewCodexToolCorrector()
	b := &hunterBody{}
	blob := turnStateFernetBlob(time.Now(), 12)
	up := &hunterUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": []string{blob}}, Body: b}}
	g.httpUpstream = up
	s.accounts.(*hunterAccounts).onMutate = func() { require.True(t, b.closed, "body must close before pool storage") }
	p := s.proxies.(*hunterProxies).values[0]
	result := s.probe(context.Background(), a, "gpt-test", cfg, p)
	require.Empty(t, result.Error)
	require.True(t, result.Healthy)
	require.True(t, b.closed)
	require.Zero(t, b.reads)
	require.True(t, up.req.Close)
	require.True(t, HTTPUpstreamFreshConnection(up.req.Context()))
	require.True(t, HTTPUpstreamRedirectsDisabled(up.req.Context()))
	require.Empty(t, up.req.Header.Get(openAICodexTurnStateHeader))
	require.NotNil(t, up.req.Body)
	latest, _ := s.fresh(context.Background(), a.ID)
	pool, e := g.decodeTurnStatePool(latest)
	require.NoError(t, e)
	require.Len(t, pool.Candidates, 1)
	require.Equal(t, blob, pool.Candidates[0].Blob)
	raw, _ := json.Marshal(CodexTurnStatePublicExtra(latest))
	require.NotContains(t, string(raw), blob)
	require.NotContains(t, latest.Extra, CodexTurnStateObservationKey, "probe cannot overwrite the last natural observation")
}
func TestHunterManagedFieldsCannotBeInjected(t *testing.T) {
	clean := stripTurnStateRuntimeExtra(map[string]any{CodexTurnStatePoolKey: "secret", CodexTurnStateHuntKey: map[string]any{"hour_count": 0}, "other": true})
	require.Equal(t, map[string]any{"other": true}, clean)
}
