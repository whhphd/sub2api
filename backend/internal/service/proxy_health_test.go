//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type proxyHealthCacheStub struct {
	mu     sync.Mutex
	states map[int64]*ProxyHealthState
}

func (c *proxyHealthCacheStub) GetProxyHealth(_ context.Context, proxyID int64) (*ProxyHealthState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.states[proxyID], nil
}

func (c *proxyHealthCacheStub) RecordProxyFailure(_ context.Context, proxyID int64, now time.Time, _ time.Duration, threshold int, cooldown time.Duration, class, message string) (*ProxyHealthState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[proxyID]
	if state == nil {
		state = &ProxyHealthState{}
		c.states[proxyID] = state
	}
	state.ConsecutiveFailures++
	state.LastFailureAt = now
	state.LastFailureClass = class
	state.LastError = message
	if state.ConsecutiveFailures >= threshold {
		until := now.Add(cooldown)
		state.OpenUntil = &until
	}
	return state, nil
}

func (c *proxyHealthCacheStub) RecordProxySuccess(_ context.Context, proxyID int64, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.states[proxyID] = &ProxyHealthState{LastSuccessAt: now}
	return nil
}

type proxyHealthProxyRepoStub struct {
	ProxyRepository
	proxies []Proxy
}

func (r *proxyHealthProxyRepoStub) ListActive(context.Context) ([]Proxy, error) {
	return append([]Proxy(nil), r.proxies...), nil
}

type proxyHealthAccountRepoStub struct {
	AccountRepository
	accounts       []Account
	updates        map[int64]int64
	tempUnschedIDs []int64
	clearedIDs     []int64
}

func (r *proxyHealthAccountRepoStub) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r *proxyHealthAccountRepoStub) BulkUpdate(_ context.Context, ids []int64, update AccountBulkUpdate) (int64, error) {
	if len(ids) != 1 || update.ProxyID == nil {
		return 0, errors.New("unexpected bulk update")
	}
	if r.updates == nil {
		r.updates = make(map[int64]int64)
	}
	r.updates[ids[0]] = *update.ProxyID
	return 1, nil
}

func (r *proxyHealthAccountRepoStub) SetTempUnschedulable(_ context.Context, id int64, _ time.Time, _ string) error {
	r.tempUnschedIDs = append(r.tempUnschedIDs, id)
	return nil
}

func (r *proxyHealthAccountRepoStub) ClearTempUnschedulable(_ context.Context, id int64) error {
	r.clearedIDs = append(r.clearedIDs, id)
	return nil
}

type proxyHealthProberStub struct {
	failedIDs   map[string]bool
	ipapiCalls  []string
	legacyCalls int
}

func (p *proxyHealthProberStub) ProbeProxy(_ context.Context, proxyURL string) (*ProxyExitInfo, int64, error) {
	p.legacyCalls++
	return nil, 0, errors.New("legacy probe must not be called")
}

func (p *proxyHealthProberStub) ProbeProxyIPAPI(_ context.Context, proxyURL string) (*ProxyExitInfo, int64, error) {
	p.ipapiCalls = append(p.ipapiCalls, proxyURL)
	if p.failedIDs[proxyURL] {
		return nil, 0, errors.New("connection refused")
	}
	return &ProxyExitInfo{IP: "203.0.113.1"}, 10, nil
}

func activeProxy(id int64, port int) Proxy {
	return Proxy{ID: id, Status: StatusActive, Protocol: "http", Host: "127.0.0.1", Port: port}
}

func TestProxyHealthPersistentFailureRebindsOAuthForSameAccountRetry(t *testing.T) {
	oldProxy := activeProxy(1, 1001)
	newProxy := activeProxy(2, 1002)
	cache := &proxyHealthCacheStub{states: make(map[int64]*ProxyHealthState)}
	accountRepo := &proxyHealthAccountRepoStub{}
	svc := NewProxyHealthService(accountRepo, &proxyHealthProxyRepoStub{proxies: []Proxy{oldProxy, newProxy}}, nil, cache, nil)
	account := &Account{ID: 30247, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &oldProxy.ID, Proxy: &oldProxy}

	retry, handled := svc.HandleTransportFailure(context.Background(), account, errors.New("proxy connection refused"))

	require.True(t, handled)
	require.True(t, retry)
	require.Equal(t, newProxy.ID, *account.ProxyID)
	require.Equal(t, newProxy.ID, accountRepo.updates[account.ID])
	require.Empty(t, accountRepo.tempUnschedIDs)
	require.False(t, svc.IsProxyHealthy(context.Background(), oldProxy.ID))
}

func TestProxyHealthRecoveryOnlyTouchesProxyTransportQuarantine(t *testing.T) {
	failedProxy := activeProxy(10, 1010)
	healthyProxy := activeProxy(20, 1020)
	cache := &proxyHealthCacheStub{states: map[int64]*ProxyHealthState{}}
	accountRepo := &proxyHealthAccountRepoStub{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &failedProxy.ID, TempUnschedulableReason: proxyTransportUnschedReasonPrefix + " connection refused"},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &failedProxy.ID, TempUnschedulableReason: "oauth credentials invalid"},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &failedProxy.ID, TempUnschedulableReason: "usage limit reached"},
	}}
	svc := NewProxyHealthService(accountRepo, &proxyHealthProxyRepoStub{proxies: []Proxy{failedProxy, healthyProxy}}, nil, cache, nil)

	svc.recoverProxyAccounts(context.Background(), failedProxy.ID)

	require.Equal(t, map[int64]int64{1: healthyProxy.ID}, accountRepo.updates)
	require.Equal(t, []int64{1}, accountRepo.clearedIDs)
}

func TestProxyHealthActiveProbeCompletesBeforeRebinding(t *testing.T) {
	dead1 := activeProxy(31, 1031)
	dead2 := activeProxy(32, 1032)
	healthy := activeProxy(33, 1033)
	prober := &proxyHealthProberStub{failedIDs: map[string]bool{dead1.URL(): true, dead2.URL(): true}}
	cache := &proxyHealthCacheStub{states: make(map[int64]*ProxyHealthState)}
	accountRepo := &proxyHealthAccountRepoStub{accounts: []Account{
		{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &dead1.ID, TempUnschedulableReason: proxyTransportUnschedReasonPrefix + " timeout"},
	}}
	svc := NewProxyHealthService(accountRepo, &proxyHealthProxyRepoStub{proxies: []Proxy{dead1, dead2, healthy}}, prober, cache, nil)

	svc.runProbeCycle(context.Background())

	require.Len(t, prober.ipapiCalls, 3)
	require.Zero(t, prober.legacyCalls)
	require.Equal(t, healthy.ID, accountRepo.updates[99], "account must not be rebound to the second dead proxy")
}
