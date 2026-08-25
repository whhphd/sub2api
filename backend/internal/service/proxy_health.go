package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"log/slog"
)

const (
	proxyHealthFailureWindow = time.Minute
	proxyHealthCooldown      = 10 * time.Minute
	proxyHealthProbeInterval = 5 * time.Minute
	proxyHealthLeaderKey     = "proxy-health-probe"
	proxyHealthLeaderTTL     = 15 * time.Minute
)

const proxyTransportUnschedReasonPrefix = "upstream transport error (proxy/network):"

// ProxyHealthState is deliberately separate from ProxyLatencyInfo. A proxy can
// have a good quality score while being temporarily unreachable right now.
type ProxyHealthState struct {
	ConsecutiveFailures int
	FailureWindowStart  time.Time
	OpenUntil           *time.Time
	LastFailureAt       time.Time
	LastSuccessAt       time.Time
	LastFailureClass    string
	LastError           string
}

// ProxyHealthCache stores the short-lived runtime circuit state. The Redis
// implementation updates failures atomically so multiple gateway instances do
// not lose increments.
type ProxyHealthCache interface {
	GetProxyHealth(ctx context.Context, proxyID int64) (*ProxyHealthState, error)
	RecordProxyFailure(ctx context.Context, proxyID int64, now time.Time, window time.Duration, threshold int, cooldown time.Duration, failureClass, message string) (*ProxyHealthState, error)
	RecordProxySuccess(ctx context.Context, proxyID int64, now time.Time) error
}

// IPAPIProxyProber is implemented by the repository probe service. It keeps
// the automatic health check limited to ip-api; the existing admin probe API
// retains its configured/fallback targets.
type IPAPIProxyProber interface {
	ProbeProxyIPAPI(ctx context.Context, proxyURL string) (*ProxyExitInfo, int64, error)
}

// ProxyHealthService combines passive request observations with a periodic
// ip-api check. It only repairs active OpenAI OAuth accounts quarantined for a
// proxy/network transport reason.
type ProxyHealthService struct {
	accountRepo AccountRepository
	proxyRepo   ProxyRepository
	prober      ProxyExitInfoProber
	healthCache ProxyHealthCache
	leaderLock  LeaderLockCache

	runtimeBlocker AccountRuntimeBlocker
	instanceID     string
	stopOnce       sync.Once
	stop           chan struct{}
	wg             sync.WaitGroup
}

func NewProxyHealthService(accountRepo AccountRepository, proxyRepo ProxyRepository, prober ProxyExitInfoProber, healthCache ProxyHealthCache, leaderLock LeaderLockCache) *ProxyHealthService {
	host, _ := os.Hostname()
	if host == "" {
		host = "sub2api"
	}
	return &ProxyHealthService{
		accountRepo: accountRepo,
		proxyRepo:   proxyRepo,
		prober:      prober,
		healthCache: healthCache,
		leaderLock:  leaderLock,
		instanceID:  fmt.Sprintf("%s-%d", host, time.Now().UnixNano()),
		stop:        make(chan struct{}),
	}
}

func (s *ProxyHealthService) SetRuntimeBlocker(blocker AccountRuntimeBlocker) {
	if s != nil {
		s.runtimeBlocker = blocker
	}
}

func (s *ProxyHealthService) Start() {
	if s == nil || s.healthCache == nil || s.proxyRepo == nil || s.prober == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Probe once on startup so dead proxies are not reused for the first
		// five minutes after a deploy.
		s.runProbeCycle(context.Background())
		ticker := time.NewTicker(proxyHealthProbeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.runProbeCycle(context.Background())
			case <-s.stop:
				return
			}
		}
	}()
}

func (s *ProxyHealthService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

func (s *ProxyHealthService) IsProxyHealthy(ctx context.Context, proxyID int64) bool {
	if s == nil || proxyID <= 0 || s.healthCache == nil {
		return true
	}
	state, err := s.healthCache.GetProxyHealth(ctx, proxyID)
	if err != nil || state == nil || state.OpenUntil == nil {
		return true
	}
	return !state.OpenUntil.After(time.Now())
}

func (s *ProxyHealthService) ChooseHealthyProxy(ctx context.Context, excludeID int64) (*Proxy, bool) {
	if s == nil || s.proxyRepo == nil {
		return nil, false
	}
	proxies, err := s.proxyRepo.ListActive(ctx)
	if err != nil {
		return nil, false
	}
	now := time.Now()
	candidates := make([]Proxy, 0, len(proxies))
	for _, proxy := range proxies {
		if proxy.ID <= 0 || proxy.ID == excludeID || !proxy.IsActive() || proxy.IsExpired(now) || !s.IsProxyHealthy(ctx, proxy.ID) {
			continue
		}
		candidates = append(candidates, proxy)
	}
	if len(candidates) == 0 {
		return nil, false
	}
	selected := candidates[rand.Intn(len(candidates))]
	return &selected, true
}

// HandleTransportFailure returns (retrySameAccount, handled). handled is true
// when this service owns the OAuth+proxy decision, so the legacy per-account
// transport quarantine must not run a second, conflicting policy.
func (s *ProxyHealthService) HandleTransportFailure(ctx context.Context, account *Account, err error) (bool, bool) {
	if s == nil || account == nil || !account.IsOpenAIOAuth() || account.ProxyID == nil || *account.ProxyID <= 0 || s.healthCache == nil {
		return false, false
	}
	if errors.Is(err, context.Canceled) || err == nil {
		return false, true
	}
	class := classifyOpenAITransportError(err)
	threshold := 2
	failureClass := "transient"
	if class.Persistent {
		threshold = 1
		failureClass = "persistent"
	}
	now := time.Now()
	state, cacheErr := s.healthCache.RecordProxyFailure(ctx, *account.ProxyID, now, proxyHealthFailureWindow, threshold, proxyHealthCooldown, failureClass, sanitizeUpstreamErrorMessage(err.Error()))
	if cacheErr != nil {
		return false, false
	}
	if state == nil {
		return false, false
	}
	if state == nil || state.OpenUntil == nil || !state.OpenUntil.After(now) {
		slog.Warn("openai.proxy_health_failure", "account_id", account.ID, "proxy_id", *account.ProxyID, "failure_class", failureClass, "consecutive_failures", state.ConsecutiveFailures)
		return false, true
	}

	proxyID := *account.ProxyID
	if rebound := s.rebindAccount(ctx, account); rebound {
		slog.Warn("openai.proxy_account_rebound", "account_id", account.ID, "from_proxy_id", proxyID, "reason", "proxy_health_circuit_open")
		return true, true
	}
	// No healthy egress is available. Preserve the old protection, but only for
	// this proxy/network reason; a later active probe can recover it.
	s.tempUnschedule(ctx, account, state.LastError)
	return false, true
}

func (s *ProxyHealthService) RecordSuccess(ctx context.Context, account *Account) {
	if s == nil || account == nil || account.ProxyID == nil || *account.ProxyID <= 0 || s.healthCache == nil {
		return
	}
	_ = s.healthCache.RecordProxySuccess(ctx, *account.ProxyID, time.Now())
}

func (s *ProxyHealthService) rebindAccount(ctx context.Context, account *Account) bool {
	if s.accountRepo == nil || account == nil || account.ProxyID == nil {
		return false
	}
	selected, ok := s.ChooseHealthyProxy(ctx, *account.ProxyID)
	if !ok {
		return false
	}
	proxyID := selected.ID
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAccountStateUpdateTimeout)
	defer cancel()
	updated, err := s.accountRepo.BulkUpdate(stateCtx, []int64{account.ID}, AccountBulkUpdate{ProxyID: &proxyID})
	if err != nil || updated != 1 {
		return false
	}
	oldReason := account.TempUnschedulableReason
	account.ProxyID = &proxyID
	account.Proxy = selected
	if strings.HasPrefix(oldReason, proxyTransportUnschedReasonPrefix) {
		_ = s.accountRepo.ClearTempUnschedulable(stateCtx, account.ID)
		if s.runtimeBlocker != nil {
			s.runtimeBlocker.ClearAccountSchedulingBlock(account.ID)
		}
		account.TempUnschedulableUntil = nil
		account.TempUnschedulableReason = ""
	}
	return true
}

func (s *ProxyHealthService) tempUnschedule(ctx context.Context, account *Account, message string) {
	if account == nil || s.accountRepo == nil {
		return
	}
	until := time.Now().Add(openAITransportErrorTempUnschedDuration)
	reason := proxyTransportUnschedReasonPrefix + " " + message
	if s.runtimeBlocker != nil {
		s.runtimeBlocker.BlockAccountScheduling(account, until, "transport_error")
	}
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAccountStateUpdateTimeout)
	defer cancel()
	_ = s.accountRepo.SetTempUnschedulable(stateCtx, account.ID, until, reason)
}

func (s *ProxyHealthService) runProbeCycle(ctx context.Context) {
	release, acquired := tryAcquireSingletonLeaderLock(ctx, s.leaderLock, nil, proxyHealthLeaderKey, s.instanceID, proxyHealthLeaderTTL)
	if !acquired {
		return
	}
	defer release()
	proxies, err := s.proxyRepo.ListActive(ctx)
	if err != nil {
		return
	}
	for i := range proxies {
		proxy := &proxies[i]
		if proxy.ID <= 0 || proxy.IsExpired(time.Now()) {
			continue
		}
		before, _ := s.healthCache.GetProxyHealth(ctx, proxy.ID)
		var probeErr error
		if ipapi, ok := s.prober.(IPAPIProxyProber); ok {
			_, _, probeErr = ipapi.ProbeProxyIPAPI(ctx, proxy.URL())
		} else {
			_, _, probeErr = s.prober.ProbeProxy(ctx, proxy.URL())
		}
		if probeErr != nil {
			state, recordErr := s.healthCache.RecordProxyFailure(ctx, proxy.ID, time.Now(), proxyHealthFailureWindow, 1, proxyHealthCooldown, "active_probe", sanitizeUpstreamErrorMessage(probeErr.Error()))
			if recordErr == nil && state != nil && state.OpenUntil != nil {
				s.recoverProxyAccounts(ctx, proxy.ID)
			}
			continue
		}
		if err := s.healthCache.RecordProxySuccess(ctx, proxy.ID, time.Now()); err != nil {
			continue
		}
		if before != nil && (before.OpenUntil != nil || before.ConsecutiveFailures > 0) {
			s.recoverProxyAccounts(ctx, proxy.ID)
		}
	}
}

func (s *ProxyHealthService) recoverProxyAccounts(ctx context.Context, proxyID int64) {
	if s.accountRepo == nil {
		return
	}
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, PlatformOpenAI, AccountTypeOAuth, StatusActive, "", 0, "")
	if err != nil {
		return
	}
	for i := range accounts {
		account := &accounts[i]
		if account.ProxyID == nil || *account.ProxyID != proxyID || !strings.HasPrefix(account.TempUnschedulableReason, proxyTransportUnschedReasonPrefix) {
			continue
		}
		if s.rebindAccount(ctx, account) {
			slog.Info("openai.proxy_account_recovered", "account_id", account.ID, "failed_proxy_id", proxyID)
		}
	}
}

// ProxyHealthBindings attaches the optional health worker after the gateway
// and rate-limit service have been constructed, without changing their public
// constructors (which are heavily used by unit tests).
type ProxyHealthBindings struct {
	Service *ProxyHealthService
}

func ProvideProxyHealthBindings(health *ProxyHealthService, gateway *OpenAIGatewayService, rateLimit *RateLimitService) *ProxyHealthBindings {
	if health != nil {
		health.SetRuntimeBlocker(gateway)
		health.Start()
	}
	if gateway != nil {
		gateway.SetProxyHealthService(health)
	}
	if rateLimit != nil {
		rateLimit.SetProxyHealthService(health)
	}
	return &ProxyHealthBindings{Service: health}
}
