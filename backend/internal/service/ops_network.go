package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

const networkInterval = 5 * time.Second
const networkStaleAfter = 15 * time.Second

type networkCounters struct {
	At      time.Time
	Boot    float64
	Devices map[string]OpsNetworkDevice
}

type OpsNetworkService struct {
	repo                      OpsNetworkRepository
	cache                     OpsNetworkCache
	settings                  SettingRepository
	ops                       *OpsService
	serverID, endpoint, owner string
	client                    *http.Client
	mu                        sync.Mutex
	previous                  *networkCounters
	lastFlush                 time.Time
	cancel                    context.CancelFunc
	done                      chan struct{}
	startedAt                 time.Time
}

func NewOpsNetworkService(repo OpsNetworkRepository, cache OpsNetworkCache, settings SettingRepository, ops *OpsService, cfg *config.Config) *OpsNetworkService {
	s := &OpsNetworkService{repo: repo, cache: cache, settings: settings, ops: ops, owner: uuid.NewString(), startedAt: time.Now(),
		client: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if cfg != nil {
		s.serverID = strings.TrimSpace(cfg.Ops.Network.ServerID)
		s.endpoint = strings.TrimSpace(cfg.Ops.Network.ExporterURL)
	}
	return s
}

func (s *OpsNetworkService) sourceConfigured() bool {
	u, err := url.Parse(s.endpoint)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,128}$`).MatchString(s.serverID)
}

func (s *OpsNetworkService) Start() {
	if s == nil || !s.sourceConfigured() || s.cache == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(networkInterval)
		defer ticker.Stop()
		for {
			s.collect(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (s *OpsNetworkService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	ctx, c := context.WithTimeout(context.Background(), 2*time.Second)
	defer c()
	_ = s.cache.ReleaseNetworkLease(ctx, s.serverID, s.owner)
}

func (s *OpsNetworkService) loadSettings(ctx context.Context) (OpsNetworkSettings, error) {
	out := DefaultOpsNetworkSettings()
	if s.settings == nil {
		return out, nil
	}
	values, err := s.settings.GetMultiple(ctx, []string{OpsNetworkSettingsKey})
	if err != nil {
		return out, err
	}
	if raw := values[OpsNetworkSettingsKey]; raw != "" {
		if err = json.Unmarshal([]byte(raw), &out); err != nil {
			return DefaultOpsNetworkSettings(), err
		}
	}
	return out, nil
}

func (s *OpsNetworkService) GetSettings(ctx context.Context) (*OpsNetworkSettingsResponse, error) {
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := &OpsNetworkSettingsResponse{Settings: settings, ServerID: s.serverID, SourceConfigured: s.sourceConfigured(), Devices: []OpsNetworkDevice{}, AlertPresets: networkAlertPresets()}
	if s.cache != nil {
		if snapshot, e := s.cache.GetNetworkSnapshot(ctx, s.serverID); e == nil && snapshot != nil {
			out.Devices = snapshot.Devices
		}
	}
	return out, nil
}

func validateNetworkSettings(settings OpsNetworkSettings) error {
	if settings.RawRetentionHours < 1 || settings.RawRetentionHours > 24 || settings.MinuteRetentionDays < 1 || settings.MinuteRetentionDays > 365 || settings.HourlyRetentionDays < settings.MinuteRetentionDays || settings.HourlyRetentionDays > 730 {
		return fmt.Errorf("invalid network retention period")
	}
	if len(settings.Links) != 2 {
		return fmt.Errorf("public and private links are required")
	}
	seen := map[string]bool{}
	devices := map[string]bool{}
	for _, link := range settings.Links {
		if (link.ID != "public" && link.ID != "private") || seen[link.ID] {
			return fmt.Errorf("invalid network link")
		}
		seen[link.ID] = true
		if !regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,64}$`).MatchString(link.Device) {
			return fmt.Errorf("invalid network interface")
		}
		for _, v := range []float64{link.RXCapacityMbps, link.TXCapacityMbps} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 1000000 {
				return fmt.Errorf("capacity must be between 0 and 1000000 Mbps")
			}
		}
		if link.Enabled {
			if devices[link.Device] {
				return fmt.Errorf("network interfaces cannot be counted twice")
			}
			devices[link.Device] = true
		}
	}
	return nil
}

func (s *OpsNetworkService) UpdateSettings(ctx context.Context, settings OpsNetworkSettings) (*OpsNetworkSettingsResponse, error) {
	if err := validateNetworkSettings(settings); err != nil {
		return nil, infraerrors.BadRequest("INVALID_NETWORK_SETTINGS", err.Error())
	}
	current, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, dev := range current.Devices {
		known[dev.Name] = true
	}
	for _, link := range settings.Links {
		unchanged := false
		for _, old := range current.Settings.Links {
			if old.ID == link.ID && old.Device == link.Device && old.Enabled == link.Enabled {
				unchanged = true
			}
		}
		if settings.Enabled && link.Enabled && !unchanged && !known[link.Device] {
			return nil, infraerrors.BadRequest("INVALID_NETWORK_INTERFACE", "Choose a discovered network interface")
		}
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	if err = s.settings.Set(ctx, OpsNetworkSettingsKey, string(raw)); err != nil {
		return nil, err
	}
	return s.GetSettings(ctx)
}

func (s *OpsNetworkService) GetOverview(ctx context.Context) (*OpsNetworkOverview, error) {
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := &OpsNetworkOverview{ServerID: s.serverID, Status: "unconfigured", Devices: []OpsNetworkDevice{}, Links: []OpsNetworkSample{}}
	if s.sourceConfigured() {
		out.Status = "collecting"
		if s.cache != nil {
			snapshot, e := s.cache.GetNetworkSnapshot(ctx, s.serverID)
			if e != nil {
				return nil, e
			}
			if snapshot != nil {
				out = snapshot
			}
		}
	}
	if !settings.Enabled {
		out.Status = "disabled"
	} else if !out.CollectedAt.IsZero() && time.Since(out.CollectedAt) > networkStaleAfter {
		out.Status = "stale"
	} else if out.Status == "collecting" && time.Since(s.startedAt) > networkStaleAfter {
		out.Status = "stale"
	}
	old := out.Links
	out.Links = []OpsNetworkSample{}
	for _, link := range settings.Links {
		sample := OpsNetworkSample{OpsNetworkLink: link, Status: out.Status}
		if sample.Status == "ok" {
			sample.Status = "collecting"
		}
		for _, previous := range old {
			if previous.ID == link.ID && previous.Device == link.Device {
				sample = previous
				sample.OpsNetworkLink = link
				break
			}
		}
		if !settings.Enabled || !link.Enabled {
			sample.Status = "disabled"
		} else if out.Status != "ok" {
			sample.Status = out.Status
		}
		if sample.Status != "ok" {
			sample.RXMbps = nil
			sample.TXMbps = nil
		}
		out.Links = append(out.Links, sample)
	}
	return out, nil
}

func (s *OpsNetworkService) collect(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	if s.ops != nil && !s.ops.IsMonitoringEnabled(ctx) {
		s.previous = nil
		return
	}
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return
	}
	ok, err := s.cache.RenewNetworkLease(ctx, s.serverID, s.owner)
	if err != nil || !ok {
		s.previous = nil
		return
	}
	counters, err := s.scrape(ctx)
	now := time.Now()
	snapshot := &OpsNetworkOverview{ServerID: s.serverID, CollectedAt: now, Status: "ok", Devices: []OpsNetworkDevice{}, Links: []OpsNetworkSample{}}
	if err != nil {
		snapshot.Status = "error"
		s.previous = nil
	} else {
		snapshot.CollectedAt = counters.At
		for _, d := range counters.Devices {
			snapshot.Devices = append(snapshot.Devices, d)
		}
		sort.Slice(snapshot.Devices, func(i, j int) bool { return snapshot.Devices[i].Name < snapshot.Devices[j].Name })
	}
	for _, link := range settings.Links {
		sample := OpsNetworkSample{OpsNetworkLink: link, Start: snapshot.CollectedAt, End: snapshot.CollectedAt, Status: snapshot.Status}
		if !settings.Enabled || !link.Enabled {
			sample.Status = "disabled"
		} else if err == nil {
			sample = calculateNetworkSample(link, s.previous, counters)
		}
		snapshot.Links = append(snapshot.Links, sample)
	}
	committed, e := s.cache.CommitNetworkSnapshot(ctx, s.serverID, s.owner, snapshot, time.Duration(settings.RawRetentionHours)*time.Hour)
	if e != nil || !committed {
		s.previous = nil
		return
	}
	s.previous = counters
	if now.Sub(s.lastFlush) >= time.Minute {
		if err := s.flush(ctx, now, settings); err != nil {
			logger.LegacyPrintf("service.ops_network", "network metrics persistence failed: %v", err)
		} else {
			s.lastFlush = now
		}
	}
}

func calculateNetworkSample(link OpsNetworkLink, previous, current *networkCounters) OpsNetworkSample {
	out := OpsNetworkSample{OpsNetworkLink: link, Start: current.At, End: current.At, Status: "collecting"}
	device, ok := current.Devices[link.Device]
	if !ok {
		out.Status = "missing"
		return out
	}
	if !device.Up {
		out.Status = "down"
		return out
	}
	if previous == nil || previous.Boot != current.Boot {
		return out
	}
	old, ok := previous.Devices[link.Device]
	if !ok || !old.Up || old.Index != device.Index {
		return out
	}
	dt := current.At.Sub(previous.At).Seconds()
	if dt <= 0 || dt > networkStaleAfter.Seconds() || device.RXBytes < old.RXBytes || device.TXBytes < old.TXBytes {
		return out
	}
	out.Start = previous.At
	out.ValidSeconds = dt
	out.RXBytes = device.RXBytes - old.RXBytes
	out.TXBytes = device.TXBytes - old.TXBytes
	rx, tx := out.RXBytes*8/dt/1e6, out.TXBytes*8/dt/1e6
	out.RXMbps = &rx
	out.TXMbps = &tx
	out.Status = "ok"
	return out
}

func (s *OpsNetworkService) flush(ctx context.Context, now time.Time, settings OpsNetworkSettings) error {
	start := now.Add(-time.Duration(settings.RawRetentionHours) * time.Hour).Truncate(time.Minute).Add(time.Minute)
	if !s.lastFlush.IsZero() {
		start = s.lastFlush.Add(-time.Minute).Truncate(time.Minute)
	}
	samples, err := s.cache.GetNetworkSamples(ctx, s.serverID, start.Add(-networkStaleAfter), now)
	if err != nil {
		return err
	}
	buckets := aggregateNetworkSamples(samples, s.serverID, "", start, now, 60)
	if err = s.repo.UpsertNetworkMinutes(ctx, buckets); err != nil {
		return err
	}
	return s.repo.AggregateNetworkHours(ctx, s.serverID, start, now)
}

func (s *OpsNetworkService) Cleanup(ctx context.Context) error {
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	return s.repo.CleanupNetworkMetrics(ctx, now.AddDate(0, 0, -settings.MinuteRetentionDays), now.AddDate(0, 0, -settings.HourlyRetentionDays))
}
