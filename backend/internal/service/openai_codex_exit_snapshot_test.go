package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexExitTestCache struct {
	mu        sync.Mutex
	snapshots map[string]*CodexExitSnapshot
}

func (c *codexExitTestCache) GetCodexExitSnapshot(_ context.Context, tag string) (*CodexExitSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshots[tag], nil
}
func (c *codexExitTestCache) SetCodexExitSnapshot(_ context.Context, s *CodexExitSnapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshots[s.ProxyTag] = s
	return nil
}

type codexExitTestProber struct {
	started chan string
	finish  chan struct{}
	calls   atomic.Int32
	err     error
}

func (p *codexExitTestProber) ProbeProxy(context.Context, string) (*ProxyExitInfo, int64, error) {
	return nil, 0, errors.New("general probe must not be used")
}
func (p *codexExitTestProber) ProbeProxyIPAPI(ctx context.Context, url string) (*ProxyExitInfo, int64, error) {
	p.calls.Add(1)
	p.started <- url
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-p.finish:
	}
	if p.err != nil {
		return nil, 0, p.err
	}
	return &ProxyExitInfo{IP: "192.0.2.1", Timezone: "America/Toronto", CountryCode: "CA", Region: "Ontario", City: "Toronto"}, 1, nil
}

type codexExitTestRepo struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
	writes  int
}

func (r *codexExitTestRepo) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return snapshotOpenAIOutboundAccount(r.account), nil
}
func (r *codexExitTestRepo) UpdateCodexExitSnapshot(_ context.Context, _ *Account, updates map[string]any) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	for k, v := range updates {
		r.account.Extra[k] = v
	}
	return true, nil
}
func (r *codexExitTestRepo) writeCount() int { r.mu.Lock(); defer r.mu.Unlock(); return r.writes }

func newCodexExitTest(t *testing.T) (*OpenAIGatewayService, *Account, *codexExitTestProber, *codexExitTestRepo, *codexExitTestCache) {
	t.Helper()
	id := int64(1)
	a := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, codexFingerprintEnhanced: true, ProxyID: &id, Proxy: &Proxy{ID: id, Protocol: "http", Host: "proxy.invalid", Port: 8080}, Extra: map[string]any{codexFingerprintConvergenceExtraKey: true}}
	prober := &codexExitTestProber{started: make(chan string, 8), finish: make(chan struct{})}
	repo := &codexExitTestRepo{account: snapshotOpenAIOutboundAccount(a)}
	cache := &codexExitTestCache{snapshots: map[string]*CodexExitSnapshot{}}
	gateway := &OpenAIGatewayService{accountRepo: repo, codexExitRefresh: newCodexExitRefreshState(), proxyHealthService: &ProxyHealthService{prober: prober, codexExitCache: cache}}
	return gateway, a, prober, repo, cache
}

func TestCodexExitLookupIsAsyncCoalescedAndOutlivesClientCancellation(t *testing.T) {
	g, a, p, r, cache := newCodexExitTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() { g.enrichCodexExitSnapshot(ctx, a); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("request waited for external lookup")
	}
	select {
	case url := <-p.started:
		require.Equal(t, a.Proxy.URL(), url)
	case <-time.After(time.Second):
		t.Fatal("lookup not started")
	}
	for i := 0; i < 20; i++ {
		g.enrichCodexExitSnapshot(context.Background(), snapshotOpenAIOutboundAccount(a))
	}
	cancel()
	close(p.finish)
	require.Eventually(t, func() bool { return r.writeCount() == 1 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, int32(1), p.calls.Load())
	got, err := cache.GetCodexExitSnapshot(context.Background(), codexWireTimezoneProxyTag(a))
	require.NoError(t, err)
	require.Equal(t, "America/Toronto", got.Timezone)
	g.enrichCodexExitSnapshot(context.Background(), a)
	require.Equal(t, "America/Toronto", codexWireTimezoneName(a))
}

func TestCodexExitLateOldProxyResultDoesNotPersistToChangedAccount(t *testing.T) {
	g, a, p, r, _ := newCodexExitTest(t)
	g.enrichCodexExitSnapshot(context.Background(), a)
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("lookup not started")
	}
	r.mu.Lock()
	id := int64(2)
	r.account.ProxyID = &id
	r.account.Proxy = &Proxy{ID: id, Protocol: "http", Host: "new.invalid", Port: 8081}
	r.mu.Unlock()
	close(p.finish)
	require.Eventually(t, func() bool { return len(g.codexExitRefresh.slots) == 0 }, time.Second, 10*time.Millisecond)
	require.Zero(t, r.writeCount())
}

func TestCodexExitFailureLeavesClientEnvironmentAndReleasesWorker(t *testing.T) {
	g, a, p, r, _ := newCodexExitTest(t)
	p.err = errors.New("unavailable")
	close(p.finish)
	g.enrichCodexExitSnapshot(context.Background(), a)
	require.Eventually(t, func() bool { return len(g.codexExitRefresh.slots) == 0 }, time.Second, 10*time.Millisecond)
	require.Zero(t, r.writeCount())
	require.Empty(t, codexWireTimezoneName(a))
}

func TestCodexExitProjectionRejectsExpiredAndWrongProxySnapshots(t *testing.T) {
	_, a, _, _, _ := newCodexExitTest(t)
	a.Extra = codexWireTimezoneExtraUpdates(codexWireTimezoneProxyTag(a), codexWireExit{timezone: "America/Toronto"}, time.Now().Add(-25*time.Hour))
	require.Empty(t, codexWireTimezoneName(a))
	a.Extra[codexWireTimezoneResolvedAtExtraKey] = time.Now().UTC().Format(time.RFC3339)
	require.Equal(t, "America/Toronto", codexWireTimezoneName(a))
	a.Proxy.Host = "changed.invalid"
	require.Empty(t, codexWireTimezoneName(a))
}

func TestCodexExitProxyEditRefreshUsesNewSnapshotAndSurvivesCallerCancel(t *testing.T) {
	g, _, p, r, _ := newCodexExitTest(t)
	g.settingService = &SettingService{settingRepo: newOpenAIOAuthRuntimeSettingRepo()}
	yes := true
	_, err := g.settingService.UpdateOpenAIOAuthRuntimeSettings(context.Background(), nil, nil, nil, nil, nil, nil, &yes)
	require.NoError(t, err)
	r.mu.Lock()
	r.account.codexFingerprintEnhanced = false // DB rows have no transient policy.
	r.account.Proxy = nil
	r.account.ProxyID = nil // Explicit clear must observe direct exit, not the old proxy.
	r.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.RefreshCodexExitSnapshot(ctx, 42)
	select {
	case url := <-p.started:
		require.Empty(t, url)
	case <-time.After(time.Second):
		t.Fatal("proxy edit did not refresh")
	}
	close(p.finish)
	require.Eventually(t, func() bool { return r.writeCount() == 1 }, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return len(g.codexExitRefresh.changes) == 0 && len(g.codexExitRefresh.slots) == 0 }, time.Second, 10*time.Millisecond)
}

func TestCodexExitSnapshotFreshRejectsFutureAndExpired(t *testing.T) {
	require.True(t, codexExitSnapshotFresh(&CodexExitSnapshot{SampledAt: time.Now().Add(-time.Second)}))
	require.False(t, codexExitSnapshotFresh(&CodexExitSnapshot{SampledAt: time.Now().Add(time.Minute)}))
	require.False(t, codexExitSnapshotFresh(&CodexExitSnapshot{SampledAt: time.Now().Add(-24 * time.Hour)}))
}
