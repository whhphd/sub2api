// SPDX-License-Identifier: LGPL-3.0-only
// Coordinator adapted from KlN klno.12 (2916a74b3): expiry/idle gates,
// per-account/model round robin, fresh proxy connections and bounded retries.
// CallAI additions: global policy/budget, encrypted storage and independent logs.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const hunterBudgetKey = "openai_turn_state_hunter_budget"
const hunterLeaderKey = "openai:turn-state-hunter:leader"

type hunterTraffic struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (t *hunterTraffic) note(id int64, model string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[string]time.Time{}
	}
	for k, at := range t.seen {
		if now.Sub(at) > 24*time.Hour {
			delete(t.seen, k)
		}
	}
	key := strconv.FormatInt(id, 10) + "\x00" + model
	if len(t.seen) < 16384 || !t.seen[key].IsZero() {
		t.seen[key] = now
	}
}
func (t *hunterTraffic) active(id int64, model string, since time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen[strconv.FormatInt(id, 10)+"\x00"+model].After(since)
}
func (s *OpenAIGatewayService) hunterPolicy(ctx context.Context) (TurnStateHunterSettings, error) {
	if s == nil || s.settingService == nil {
		return TurnStateHunterSettings{}, errors.New("settings unavailable")
	}
	op, cancel := context.WithTimeout(ctx, turnStateTimeout)
	defer cancel()
	p, err := s.settingService.readOpenAIOAuthRuntimeSettings(op)
	if err != nil {
		return TurnStateHunterSettings{}, err
	}
	cfg := p.TurnStateHunter.clone()
	if err = cfg.validate(); err != nil {
		return TurnStateHunterSettings{}, err
	}
	cfg.Enabled = cfg.Enabled && p.TurnStateAutoEnabled && s.turnStateEncryptor != nil && s.cfg != nil && s.cfg.Totp.EncryptionKeyConfigured
	return cfg, nil
}
func sameHunterPolicy(a, b TurnStateHunterSettings) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Normalized DNS boundaries prevent unrelated hosts being treated as providers.
// 1024 rotating usernames omit sid/t; sticky credentials are never rewritten.
func openAITurnStateHuntProxyRotating(p Proxy) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(p.Host)), ".")
	u := strings.ToLower(p.Username)
	within := func(d string) bool { return h == d || strings.HasSuffix(h, "."+d) }
	if within("webshare.io") {
		return strings.HasSuffix(u, "-rotate")
	}
	return within("1024proxy.io") && u != "" && !strings.Contains(u, "-sid-") && !strings.Contains(u, "-t-")
}

type OpenAITurnStateHunterService struct {
	gateway     *OpenAIGatewayService
	accounts    AccountRepository
	proxies     ProxyRepository
	prober      IPAPIProxyProber
	leader      LeaderLockCache
	owner       string
	cursor      int64
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	start, stop sync.Once
	// Offline tests inject the paid-probe boundary and clock, never a real AI endpoint.
	probeOverride func(context.Context, *Account, string, TurnStateHunterSettings, Proxy) openAITurnStateHuntAttempt
	now           func() time.Time
 retryWait     func(context.Context) error
}

func NewOpenAITurnStateHunterService(g *OpenAIGatewayService, a AccountRepository, p ProxyRepository, prober IPAPIProxyProber, leader LeaderLockCache) *OpenAITurnStateHunterService {
	ctx, cancel := context.WithCancel(context.Background())
	return &OpenAITurnStateHunterService{gateway: g, accounts: a, proxies: p, prober: prober, leader: leader, owner: uuid.NewString(), ctx: ctx, cancel: cancel, now: time.Now}
}
func (s *OpenAITurnStateHunterService) Start() {
	s.start.Do(func() {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-s.ctx.Done():
					return
				case <-ticker.C:
					s.runOnce(s.ctx)
				}
			}
		}()
	})
}
func (s *OpenAITurnStateHunterService) Stop() {
	if s != nil {
		s.stop.Do(func() { s.cancel(); s.wg.Wait() })
	}
}
func (s *OpenAITurnStateHunterService) log(a *Account, model, event, reason string, extra map[string]any) {
	attempt := &turnStateAttempt{Model: model, Probe: true, Enabled: true}
	if a != nil {
		attempt.AccountID = a.ID
		attempt.Owner = turnStateOwner(a)
	}
	s.gateway.logTurnState(attempt, event, reason, extra)
}
func (s *OpenAITurnStateHunterService) runOnce(parent context.Context) {
	defer func() {
		if recover() != nil {
			s.log(nil, "", "hunter_error", "unexpected_panic", nil)
		}
	}()
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	cfg, err := s.gateway.hunterPolicy(ctx)
	if err != nil || !cfg.Enabled {
		return
	}
	// A failed lock never permits spending. No best-effort unlocked fallback.
	if s.leader == nil {
		return
	}
	ok, err := s.leader.TryAcquireLeaderLock(ctx, hunterLeaderKey, s.owner, 20*time.Minute)
	if err != nil || !ok {
		return
	}
	defer func() {
		c, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_ = s.leader.ReleaseLeaderLock(c, hunterLeaderKey, s.owner)
	}()
	readCtx, done := context.WithTimeout(ctx, 5*time.Second)
	accounts, err := s.accounts.ListByPlatform(readCtx, PlatformOpenAI)
	done()
	if err != nil {
		s.log(nil, "", "hunter_error", "accounts_unavailable", nil)
		return
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	// One bounded probe sequence per account per pass prevents budget monopolization.
	for len(accounts) > 0 && ctx.Err() == nil {
		progressed := false
		start := sort.Search(len(accounts), func(i int) bool { return accounts[i].ID > s.cursor }) % len(accounts)
		for n := 0; n < len(accounts); n++ {
			if ctx.Err() != nil {
				return
			}
			a := accounts[(start+n)%len(accounts)]
			if a.Status != StatusActive || turnStateOwner(&a) == "" {
				continue
			}
			current, e := s.gateway.hunterPolicy(ctx)
			if e != nil || !current.Enabled || !sameHunterPolicy(current, cfg) {
				return
			}
			s.cursor = a.ID
			spent, halt := s.huntOne(ctx, &a, cfg)
			if halt {
				return
			}
			if !spent {
				continue
			}
			progressed = true
			timer := time.NewTimer(openAITurnStateHuntJitter(time.Duration(cfg.GapSeconds) * time.Second))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if !progressed {
			return
		}
	}
}
func (s *OpenAITurnStateHunterService) fresh(ctx context.Context, id int64) (*Account, error) {
	op, cancel := context.WithTimeout(ctx, turnStateTimeout)
	defer cancel()
	return s.accounts.GetByID(op, id)
}
func (s *OpenAITurnStateHunterService) save(ctx context.Context, a *Account, st openAITurnStateHuntState) error {
	store, ok := s.accounts.(CodexTurnStateStore)
	if !ok {
		return errors.New("storage unavailable")
	}
	st.Owner = turnStateOwner(a)
	st.UpdatedAt = s.now()
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	var data map[string]any
	if err = json.Unmarshal(raw, &data); err != nil {
		return err
	}
	op, cancel := context.WithTimeout(ctx, turnStateTimeout)
	defer cancel()
	err = store.MutateCodexTurnState(op, a.ID, func(latest *Account) (map[string]any, error) {
		if turnStateOwner(latest) != st.Owner {
			return nil, errors.New("identity changed")
		}
		return map[string]any{CodexTurnStateHuntKey: data}, nil
	})
	if err != nil {
		s.log(a, "", "hunter_storage_error", "state_not_saved", nil)
	}
	return err
}
func (s *OpenAITurnStateHunterService) gate(ctx context.Context, a *Account, st openAITurnStateHuntState, reason string) {
	if st.Gate == reason {
		return
	}
	st.Gate = reason
	_ = s.save(ctx, a, st)
	s.log(a, "", "hunter_gate", reason, nil)
}
func (s *OpenAITurnStateHunterService) reserveGlobal(ctx context.Context, cfg TurnStateHunterSettings) (bool, error) {
	repo := s.gateway.settingService.settingRepo
	cas, ok := repo.(SettingCompareAndSwapper)
	if !ok {
		return false, errors.New("atomic settings unavailable")
	}
	op, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		raw, err := repo.GetValue(op, hunterBudgetKey)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return false, err
		}
		var st struct {
			Start time.Time `json:"start"`
			Count int       `json:"count"`
		}
		if raw != "" {
			if err = json.Unmarshal([]byte(raw), &st); err != nil {
				return false, err
			}
		}
		if st.Start.IsZero() || !s.now().Before(st.Start.Add(time.Hour)) {
			st.Start = s.now()
			st.Count = 0
		}
		if st.Count >= cfg.MaxPerHour {
			return false, nil
		}
		st.Count++
		next, _ := json.Marshal(st)
		swapped, err := cas.CompareAndSwap(op, hunterBudgetKey, raw, string(next))
		if err != nil {
			return false, err
		}
		if swapped {
			return true, nil
		}
	}
	return false, errors.New("budget contention")
}
func (s *OpenAITurnStateHunterService) huntOne(ctx context.Context, old *Account, cfg TurnStateHunterSettings) (spent, halt bool) {
	a, err := s.fresh(ctx, old.ID)
	if err != nil || a.Status != StatusActive || turnStateOwner(a) == "" || turnStateOwner(a) != turnStateOwner(old) {
		return false, false
	}
	st := readOpenAITurnStateHuntState(a)
	now := s.now()
	st.rollHour(now)
	if st.waiting(cfg, now) {
		return false, false
	}
	if st.HourCount >= cfg.PerAccountMaxPerHour {
		st.CapWait = true
		st.NextAt = st.HourStart.Add(time.Hour)
		s.gate(ctx, a, st, "account_cap")
		return false, false
	}
	pool, err := s.gateway.decodeTurnStatePool(a)
	if err != nil {
		s.gate(ctx, a, st, "pool_unavailable")
		return false, false
	}
	models := []string{}
	active := false
	for _, model := range cfg.Models {
		if cfg.IdleMinutes > 0 && !s.gateway.turnStateTraffic.active(a.ID, model, now.Add(-time.Duration(cfg.IdleMinutes)*time.Minute)) {
			continue
		}
		active = true
		expiry := time.Time{}
		for _, c := range pool.Candidates {
			if c.usable(model, now) && !now.Before(pool.Rejected[turnStateRejectionKey(c.Model, c.Blob)]) && c.MintedAt.Add(turnStateTTL).After(expiry) {
				expiry = c.MintedAt.Add(turnStateTTL)
			}
		}
		if expiry.Sub(now) <= time.Duration(cfg.LeadMinutes)*time.Minute {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		if active {
			s.gate(ctx, a, st, openAITurnStateHuntGateFresh)
		} else {
			s.gate(ctx, a, st, openAITurnStateHuntGateIdle)
		}
		return false, false
	}
	op, cancel := context.WithTimeout(ctx, turnStateTimeout)
	proxies, err := s.proxies.ListByIDs(op, cfg.ProxyIDs)
	cancel()
	if err != nil {
		s.gate(ctx, a, st, "proxy_unavailable")
		return false, false
	}
	var selected *Proxy
	exit := ""
	// Cursor rotates both targets; transport retries stay in the same bounded sequence.
	model := models[st.Cursor%len(models)]
	for n := 0; n < len(cfg.ProxyIDs); n++ {
		id := cfg.ProxyIDs[(st.Cursor+n)%len(cfg.ProxyIDs)]
		for _, p := range proxies {
			if p.ID != id || !p.IsActive() || p.IsExpired(now) {
				continue
			}
			exit = ""
			if !openAITurnStateHuntProxyRotating(p) {
				if s.prober == nil {
					continue
				}
				proxyURL, e := resolveConfiguredProxyURL(ctx, nil, &p.ID, &p)
				if e != nil {
					continue
				}
				echo, stop := context.WithTimeout(ctx, 15*time.Second)
				info, _, e := s.prober.ProbeProxyIPAPI(echo, proxyURL)
				stop()
				if e != nil || info == nil || info.IP == "" {
					continue
				}
				exit = info.IP
				if st.exitCoolingDown(exit, now) {
					continue
				}
				// Fixed exit only once in each retry window, including a successful probe.
				recent := false
				for _, v := range st.Exits {
					if v.IP == exit && now.Sub(v.At) < time.Duration(cfg.RetryMinutes)*time.Minute {
						recent = true
						break
					}
				}
				if recent {
					continue
				}
			}
			copy := p
			selected = &copy
			st.Cursor += n + 1
			break
		}
		if selected != nil {
			break
		}
	}
	if selected == nil {
		s.gate(ctx, a, st, "no_usable_exit")
		return false, false
	}
	return s.probeWithTransportRetry(ctx, a, model, cfg, *selected, exit, st)
}

// Only transport failures with no HTTP response receive short retries. All three
// attempts build new requests/connections and reserve independent budget entries.
// Backoff is persisted after each failure so interruption cannot skip it.
func (s *OpenAITurnStateHunterService) probeWithTransportRetry(ctx context.Context, account *Account, model string, cfg TurnStateHunterSettings, selected Proxy, exit string, st openAITurnStateHuntState) (spent, halt bool) {
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil { return spent, true }
		current, err := s.gateway.hunterPolicy(ctx)
		if err != nil || !sameHunterPolicy(current, cfg) { return spent, true }
		a, err := s.fresh(ctx, account.ID)
		if err != nil || a == nil || a.Status != StatusActive || turnStateOwner(a) != turnStateOwner(account) { return spent, true }
		// Reload the proxy before retrying; do not reuse a deleted/disabled or edited binding.
		if attempt > 1 {
			op, cancel := context.WithTimeout(ctx, turnStateTimeout)
			proxies, proxyErr := s.proxies.ListByIDs(op, []int64{selected.ID})
			cancel()
			if proxyErr != nil { return spent, true }
			matched := false
			for _, p := range proxies {
				if p.ID == selected.ID && p.IsActive() && !p.IsExpired(s.now()) &&
					p.Protocol == selected.Protocol && p.Host == selected.Host && p.Port == selected.Port &&
					p.Username == selected.Username && p.Password == selected.Password { matched = true; break }
			}
			if !matched { return spent, true }
		}
		st.rollHour(s.now())
		if st.HourCount >= cfg.PerAccountMaxPerHour {
			st.CapWait = true
			st.NextAt = st.HourStart.Add(time.Hour)
			s.gate(ctx, a, st, "account_cap")
			return spent, false
		}
		allowed, err := s.reserveGlobal(ctx, cfg)
		if err != nil { s.log(a, model, "hunter_storage_error", "budget_unavailable", nil); return spent, true }
		if !allowed { s.gate(ctx, a, st, "global_cap"); return spent, true }
		st.HourCount++
		st.CapWait = false
		st.Gate = ""
		st.NextAt = time.Time{}
		if s.save(ctx, a, st) != nil { return spent, true }
		var result openAITurnStateHuntAttempt
		if s.probeOverride != nil { result = s.probeOverride(ctx, a, model, cfg, selected) } else { result = s.probe(ctx, a, model, cfg, selected) }
		spent = true
		result.Exit = exit
		result.RetryAttempt = attempt
		st.HourCount-- // push consumes the allowance reserved above.
		st.push(result)
		transportFailure := result.Status == 0 && result.Error == "transport_error"
		if transportFailure { st.NextAt = s.now().Add(time.Minute) } else if result.Status != http.StatusOK || result.Error != "" { st.NextAt = s.now().Add(openAITurnStateHuntBackoff(result.Status)) }
		s.log(a, model, "hunter_attempt", result.Error, map[string]any{"proxy_id": result.ProxyID, "http_status": result.Status, "length": result.Chars, "baseline": result.Healthy, "headers_ms": result.LatencyMs, "hour_count": st.HourCount, "retry_attempt": attempt})
		if s.save(ctx, a, st) != nil { return spent, true }
		if ctx.Err() != nil { return spent, true }
		if !transportFailure || attempt == maxAttempts { return spent, false }
		s.log(a, model, "hunter_retry", "transport_error", map[string]any{"proxy_id": selected.ID, "next_attempt": attempt+1, "delay_seconds": 2})
		if err := s.waitTransportRetry(ctx); err != nil { return spent, true }
	}
	return spent, false
}

func (s *OpenAITurnStateHunterService) waitTransportRetry(ctx context.Context) error {
	if s.retryWait != nil { return s.retryWait(ctx) }
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select { case <-ctx.Done(): return ctx.Err(); case <-timer.C: return nil }
}

func (s *OpenAITurnStateHunterService) probe(ctx context.Context, a *Account, model string, cfg TurnStateHunterSettings, p Proxy) openAITurnStateHuntAttempt {
	result := openAITurnStateHuntAttempt{At: s.now(), Model: model, ProxyID: p.ID}
	proxyURL, err := resolveConfiguredProxyURL(ctx, nil, &p.ID, &p)
	if err != nil || proxyURL == "" {
		result.Error = "proxy_unavailable"
		return result
	}
	egress := snapshotOpenAIOutboundAccount(a)
	egress.ProxyID = &p.ID
	egress.Proxy = &p
	op, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	_, req, err := s.gateway.buildOpenAITurnStateProbe(op, egress, model, cfg.ReasoningEffort)
	if err != nil {
		result.Error = "build_failed"
		return result
	}
	req.Header.Del(openAICodexTurnStateHeader)
	req.Close = true
	req = req.WithContext(WithHTTPUpstreamFreshConnection(WithHTTPUpstreamRedirectsDisabled(req.Context())))
	started := s.now()
	resp, err := s.gateway.httpUpstream.Do(req, proxyURL, a.ID, a.Concurrency)
	result.LatencyMs = s.now().Sub(started).Milliseconds()
	if err != nil {
		result.Error = "transport_error"
		return result
	}
	if resp == nil || resp.Body == nil {
		result.Error = "empty_response"
		return result
	}
	result.Status = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		result.Error = diagnosticErrorClass(resp.StatusCode, peek)
		return result
	}
	// Close before DB access; never wait for text generation to finish.
	_ = resp.Body.Close()
	value := extractOpenAICodexTurnState(resp.Header)
	if value == "" || len(value) > turnStateMaxBlob {
		result.Error = "missing_or_oversize_state"
		return result
	}
	result.Chars = len(value)
	result.Healthy = openAITurnStateHealthy(value)
	if current, e := s.gateway.hunterPolicy(ctx); e != nil || !sameHunterPolicy(current, cfg) {
		result.Error = "policy_changed"
		return result
	}
	attempt := turnStateAttemptFrom(req.Context())
	if attempt == nil {
		result.Error = "identity_unavailable"
		return result
	}
	attempt.Enabled = true
	attempt.Probe = true
	s.gateway.recordTurnStateObservation(ctx, attempt, value)
	return result
}
