package service

import (
	"context"
	"golang.org/x/sync/singleflight"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CodexExitSnapshot is shared with the existing ip-api probe/cache. ProxyTag
// binds observations to proxy configuration, never to a mutable account row.
type CodexExitSnapshot struct {
	ProxyTag  string    `json:"proxy_tag"`
	IP        string    `json:"ip"`
	Timezone  string    `json:"timezone"`
	City      string    `json:"city"`
	Region    string    `json:"region"`
	Country   string    `json:"country"`
	SampledAt time.Time `json:"sampled_at"`
}

type CodexExitSnapshotCache interface {
	GetCodexExitSnapshot(context.Context, string) (*CodexExitSnapshot, error)
	SetCodexExitSnapshot(context.Context, *CodexExitSnapshot) error
}

type CodexExitSnapshotWriter interface {
	UpdateCodexExitSnapshot(context.Context, *Account, map[string]any) (bool, error)
}

type codexWireExit struct{ ip, timezone, city, region, country string }

type codexExitRefreshState struct {
	sf      singleflight.Group
	mu      sync.Mutex
	active  map[string]time.Time
	slots   chan struct{}
	changes chan struct{}
}

func newCodexExitRefreshState() *codexExitRefreshState {
	return &codexExitRefreshState{active: make(map[string]time.Time), slots: make(chan struct{}, 4), changes: make(chan struct{}, 4)}
}

// enrichCodexExitSnapshot never waits for an external lookup. The selected
// account is already an immutable request copy; only that copy is enriched.
func (s *OpenAIGatewayService) enrichCodexExitSnapshot(ctx context.Context, account *Account) {
	if s == nil || account == nil || !account.codexFingerprintEnhanced || s.proxyHealthService == nil {
		return
	}
	health := s.proxyHealthService
	if health.codexExitCache == nil || s.codexExitRefresh == nil {
		return
	}
	tag := codexWireTimezoneProxyTag(account)
	cacheCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	snapshot, err := health.codexExitCache.GetCodexExitSnapshot(cacheCtx, tag)
	cancel()
	if err == nil && snapshot != nil && snapshot.ProxyTag == tag && codexExitSnapshotFresh(snapshot) {
		updates := codexWireTimezoneExtraUpdates(tag, codexWireExit{snapshot.IP, snapshot.Timezone, snapshot.City, snapshot.Region, snapshot.Country}, snapshot.SampledAt)
		if updates != nil {
			if account.GetExtraString(codexWireTimezoneResolvedProxyExtraKey) != tag || account.GetExtraString(codexWireTimezoneResolvedAtExtraKey) != snapshot.SampledAt.Format(time.RFC3339) {
				s.scheduleCodexExitRefresh(ctx, account)
			}
			if account.Extra == nil {
				account.Extra = map[string]any{}
			}
			for k, v := range updates {
				account.Extra[k] = v
			}
			return
		}
	}
	if shouldResolveCodexWireTimezone(account, account, time.Now()) {
		s.scheduleCodexExitRefresh(ctx, account)
	}
}

func (s *OpenAIGatewayService) scheduleCodexExitRefresh(ctx context.Context, account *Account) {
	state := s.codexExitRefresh
	health := s.proxyHealthService
	if state == nil || health == nil || health.codexExitCache == nil {
		return
	}
	prober, ok := health.prober.(IPAPIProxyProber)
	if !ok {
		return
	}
	copy := snapshotOpenAIOutboundAccount(account)
	tag := codexWireTimezoneProxyTag(copy)
	if manual := strings.TrimSpace(copy.GetExtraString(codexWireTimezoneExtraKey)); manual != "" {
		if _, err := codexWireTimezoneLocation(manual); err == nil {
			return
		}
	}
	stateKey := tag + "|" + strconv.FormatInt(copy.ID, 10)
	state.mu.Lock()
	now := time.Now()
	for k, t := range state.active {
		if now.Sub(t) > time.Minute {
			delete(state.active, k)
		}
	}
	if _, exists := state.active[stateKey]; exists {
		state.mu.Unlock()
		return
	}
	select {
	case state.slots <- struct{}{}:
	default:
		state.mu.Unlock()
		return
	}
	state.active[stateKey] = now
	state.mu.Unlock()
	go func() {
		defer func() { <-state.slots; _ = recover() }()
		background, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		value, _, _ := state.sf.Do(tag, func() (any, error) {
			snapshot, err := health.codexExitCache.GetCodexExitSnapshot(background, tag)
			if err != nil {
				return nil, err
			}
			if err == nil && snapshot != nil && codexExitSnapshotFresh(snapshot) {
				return snapshot, nil
			}
			release, acquired := tryAcquireSingletonLeaderLock(background, health.leaderLock, nil, "codex-exit:"+tag, health.instanceID, 20*time.Second)
			if !acquired {
				return nil, nil
			}
			defer release()
			proxyURL, err := resolveConfiguredProxyURL(background, health.proxyRepo, copy.ProxyID, copy.Proxy)
			if err != nil {
				return nil, err
			}
			info, _, err := prober.ProbeProxyIPAPI(background, proxyURL)
			if err != nil || info == nil {
				return nil, err
			}
			if _, err = codexWireTimezoneLocation(info.Timezone); err != nil {
				return nil, err
			}
			snapshot = &CodexExitSnapshot{ProxyTag: tag, IP: info.IP, Timezone: info.Timezone, City: info.City, Region: info.Region, Country: info.CountryCode, SampledAt: time.Now()}
			if err = health.codexExitCache.SetCodexExitSnapshot(background, snapshot); err != nil {
				return nil, err
			}
			return snapshot, nil
		})
		snapshot, ok := value.(*CodexExitSnapshot)
		if !ok || snapshot == nil {
			return
		}
		// A late lookup must never label old observations as a new proxy. Projection
		// also checks this tag, including any proxy change concurrent with this read.
		latest, err := s.accountRepo.GetByID(background, copy.ID)
		if err != nil || latest == nil || codexWireTimezoneProxyTag(latest) != tag {
			return
		}
		updates := codexWireTimezoneExtraUpdates(tag, codexWireExit{snapshot.IP, snapshot.Timezone, snapshot.City, snapshot.Region, snapshot.Country}, snapshot.SampledAt)
		if updates != nil {
			if writer, ok := s.accountRepo.(CodexExitSnapshotWriter); ok {
				_, _ = writer.UpdateCodexExitSnapshot(background, copy, updates)
			}
		}
	}()
}

func codexExitSnapshotFresh(snapshot *CodexExitSnapshot) bool {
	age := time.Since(snapshot.SampledAt)
	return age >= 0 && age < codexWireTimezoneResolveTTL
}

// CodexExitSnapshotRefresher is optional on existing runtime blocker bindings.
// Proxy edits remain synchronous for persistence; observation is bounded and
// asynchronous and never changes the result of the administrative operation.
type CodexExitSnapshotRefresher interface {
	RefreshCodexExitSnapshot(context.Context, int64)
}

func notifyCodexExitProxyChange(ctx context.Context, target any, accountID int64) {
	if refresher, ok := target.(CodexExitSnapshotRefresher); ok {
		refresher.RefreshCodexExitSnapshot(ctx, accountID)
	}
}

func (s *OpenAIGatewayService) RefreshCodexExitSnapshot(ctx context.Context, accountID int64) {
	if s == nil || s.codexExitRefresh == nil || s.accountRepo == nil || s.settingService == nil {
		return
	}
	select {
	case s.codexExitRefresh.changes <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-s.codexExitRefresh.changes }()
		background, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if !s.settingService.GetOpenAIOAuthRuntimeSettings(background).CodexFingerprintEnhancementEnabled {
			return
		}
		account, err := s.accountRepo.GetByID(background, accountID)
		if err != nil || account == nil || !account.IsOpenAIOAuth() {
			return
		}
		s.prepareCodexFingerprintAccount(background, account)
	}()
}
