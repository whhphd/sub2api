// SPDX-License-Identifier: LGPL-3.0-only
// Cases adapted from KlN klno.12 openai_turn_state_hunter_test.go; CallAI global-policy harness.
package service

import (
	"context"
	"encoding/json"
	"fmt"
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
	repo, ok := s.accounts.(*hunterAccounts)
	require.True(t, ok)
	repo.fail = true
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
		require.Equal(t, tc.want, openAITurnStateHuntProxyRotating(DefaultTurnStateHunterSettings(), Proxy{Host: tc.host, Username: tc.user}))
	}
	cfg := DefaultTurnStateHunterSettings()
	cfg.ProxyIDs = []int64{9}
	cfg.RotatingProxyIDs = []int64{9}
	require.True(t, openAITurnStateHuntProxyRotating(cfg, Proxy{ID: 9, Host: "other.invalid", Username: "fixed"}))
	require.Equal(t, time.Hour, openAITurnStateHuntBackoff(429))
	require.Equal(t, 6*time.Hour, openAITurnStateHuntBackoff(401))
	now := time.Now()
	st := openAITurnStateHuntState{NextAt: now.Add(time.Hour), CapWait: true, HourCount: 30}
	cfg = DefaultTurnStateHunterSettings()
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
	repo, ok := s.accounts.(*hunterAccounts)
	require.True(t, ok)
	repo.onMutate = func() { require.True(t, b.closed, "body must close before pool storage") }
	proxies, ok := s.proxies.(*hunterProxies)
	require.True(t, ok)
	p := proxies.values[0]
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

func TestHunterHourlyLimitsHaveNoFixedCeiling(t *testing.T) {
	cfg := DefaultTurnStateHunterSettings()
	cfg.MaxPerHour, cfg.PerAccountMaxPerHour = 10000, 3000
	require.NoError(t, cfg.validate())
	for _, value := range []int{0, -1} {
		invalid := cfg
		invalid.MaxPerHour = value
		require.Error(t, invalid.validate())
		invalid = cfg
		invalid.PerAccountMaxPerHour = value
		require.Error(t, invalid.validate())
	}
}

func TestHunterTransportRetryOutcomesAndBudgets(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		statuses               []int
		global, account, calls int
		backoff                time.Duration
	}{
		{"recovers", []int{0, 200}, 1000, 1000, 2, 0},
		{"three_failures", []int{0, 0, 0, 200}, 1000, 1000, 3, time.Minute},
		{"global_budget_refunded", []int{0, 0, 200}, 1, 1000, 3, 0},
		{"account_budget_refunded", []int{0, 0, 200}, 1000, 1, 3, 0},
		{"rate_limit", []int{0, 429, 200}, 1000, 1000, 2, time.Hour},
		{"forbidden", []int{403, 200}, 1000, 1000, 1, 6 * time.Hour},
		{"unauthorized", []int{401, 200}, 1000, 1000, 1, 6 * time.Hour},
		{"upstream_error", []int{503, 200}, 1000, 1000, 1, 15 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, cfg := newHunterTest(t)
			fixed := time.Now().UTC()
			s.now = func() time.Time { return fixed }
			cfg.MaxPerHour, cfg.PerAccountMaxPerHour = tc.global, tc.account
			_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(context.Background(), &cfg, nil, nil)
			require.NoError(t, err)
			waits, calls := 0, 0
			s.retryWait = func(context.Context) error { waits++; return nil }
			s.probeOverride = func(_ context.Context, _ *Account, model string, _ TurnStateHunterSettings, p Proxy) openAITurnStateHuntAttempt {
				status := tc.statuses[calls]
				calls++
				result := openAITurnStateHuntAttempt{At: fixed, Model: model, ProxyID: p.ID, Status: status}
				if status == 0 {
					result.Error = "transport_error"
				}
				return result
			}
			spent, _ := s.huntOne(context.Background(), a, cfg)
			require.True(t, spent)
			require.Equal(t, tc.calls, calls)
			if tc.name == "three_failures" {
				require.Equal(t, 2, waits)
			}
			latest, err := s.fresh(context.Background(), a.ID)
			require.NoError(t, err)
			st := readOpenAITurnStateHuntState(latest)
			nonTransport := 0
			for _, status := range tc.statuses[:min(len(tc.statuses), calls)] {
				if status != 0 {
					nonTransport++
				}
			}
			require.Equal(t, nonTransport, st.HourCount)
			require.Len(t, st.Last, tc.calls)
			require.Equal(t, tc.calls, st.Last[0].RetryAttempt)
			if tc.backoff == 0 {
				require.True(t, st.NextAt.IsZero())
			} else {
				require.Equal(t, fixed.Add(tc.backoff), st.NextAt)
			}
			raw, err := s.gateway.settingService.settingRepo.GetValue(context.Background(), hunterBudgetKey)
			require.NoError(t, err)
			var budget struct {
				Count int `json:"count"`
			}
			require.NoError(t, json.Unmarshal([]byte(raw), &budget))
			require.Equal(t, nonTransport, budget.Count)
		})
	}
}

func TestHunterRetryRechecksCancellationPolicyAndProxy(t *testing.T) {
	for _, change := range []string{"cancel", "disable", "identity", "proxy", "storage"} {
		t.Run(change, func(t *testing.T) {
			s, a, cfg := newHunterTest(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			s.probeOverride = func(_ context.Context, _ *Account, model string, _ TurnStateHunterSettings, p Proxy) openAITurnStateHuntAttempt {
				calls++
				return openAITurnStateHuntAttempt{At: s.now(), Model: model, ProxyID: p.ID, Error: "transport_error"}
			}
			s.retryWait = func(context.Context) error {
				switch change {
				case "cancel":
					cancel()
					return ctx.Err()
				case "disable":
					cfg.Enabled = false
					_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(ctx, &cfg, nil, nil)
					require.NoError(t, err)
				case "identity":
					repo, ok := s.accounts.(*hunterAccounts)
					require.True(t, ok)
					repo.account.Credentials["chatgpt_account_id"] = "different"
				case "proxy":
					repo, ok := s.proxies.(*hunterProxies)
					require.True(t, ok)
					repo.values[0].Password = "edited"
				case "storage":
					repo, ok := s.accounts.(*hunterAccounts)
					require.True(t, ok)
					repo.fail = true
				}
				return nil
			}
			spent, halt := s.huntOne(ctx, a, cfg)
			require.True(t, spent)
			require.True(t, halt)
			require.Equal(t, 1, calls)
		})
	}
}

type retryHunterUpstream struct {
	HTTPUpstream
	requests []*http.Request
	bodies   []*hunterBody
}

func (u *retryHunterUpstream) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.requests = append(u.requests, r)
	if len(u.requests) < 3 {
		return nil, fmt.Errorf("offline transport failure")
	}
	b := &hunterBody{}
	u.bodies = append(u.bodies, b)
	return &http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": []string{turnStateFernetBlob(time.Now(), 12)}}, Body: b}, nil
}
func TestHunterRetriesCreateFreshProbeRequests(t *testing.T) {
	for _, enhanced := range []bool{false, true} {
		t.Run(fmt.Sprintf("enhancement_%v", enhanced), func(t *testing.T) {
			s, a, cfg := newHunterTest(t)
			_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, nil, nil, nil, nil, &enhanced)
			require.NoError(t, err)
			s.gateway.toolCorrector = NewCodexToolCorrector()
			upstream := &retryHunterUpstream{}
			s.gateway.httpUpstream = upstream
			s.probeOverride = nil
			s.retryWait = func(context.Context) error { return nil }
			spent, halt := s.huntOne(context.Background(), a, cfg)
			require.True(t, spent)
			require.False(t, halt)
			require.Len(t, upstream.requests, 3)
			sessions := map[string]bool{}
			for _, r := range upstream.requests {
				require.True(t, r.Close)
				require.True(t, HTTPUpstreamFreshConnection(r.Context()))
				require.True(t, HTTPUpstreamRedirectsDisabled(r.Context()))
				require.Empty(t, r.Header.Get(openAICodexTurnStateHeader))
				header := "session_id"
				if enhanced {
					header = "session-id"
				}
				session := r.Header.Get(header)
				require.NotEmpty(t, session)
				require.False(t, sessions[session])
				sessions[session] = true
			}
			require.True(t, upstream.bodies[0].closed)
			require.Zero(t, upstream.bodies[0].reads)
			latest, err := s.fresh(context.Background(), a.ID)
			require.NoError(t, err)
			pool, err := s.gateway.decodeTurnStatePool(latest)
			require.NoError(t, err)
			require.Len(t, pool.Candidates, 1)
		})
	}
}
