package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func isNetworkAlert(metric string) bool {
	return metric == "network_rx_utilization_percent" || metric == "network_tx_utilization_percent" || metric == "network_unavailable"
}

func networkAlertPresets() []*OpsAlertRule {
	out := []*OpsAlertRule{}
	for _, direction := range []string{"rx", "tx"} {
		for _, level := range []struct {
			threshold float64
			minutes   int
			severity  string
		}{{80, 3, "P1"}, {90, 1, "P0"}} {
			out = append(out, &OpsAlertRule{Name: fmt.Sprintf("Public %s bandwidth %.0f%%", direction, level.threshold), MetricType: "network_" + direction + "_utilization_percent", Operator: ">=", Threshold: level.threshold, Severity: level.severity, WindowMinutes: 1, SustainedMinutes: level.minutes, CooldownMinutes: 15, Filters: map[string]any{"network_link": "public"}})
		}
	}
	out = append(out, &OpsAlertRule{Name: "Public network monitoring unavailable", MetricType: "network_unavailable", Operator: ">=", Threshold: 1, Severity: "P1", WindowMinutes: 1, SustainedMinutes: 1, CooldownMinutes: 15, Filters: map[string]any{"network_link": "public"}})
	return out
}

func validateNetworkAlert(rule *OpsAlertRule) error {
	if rule == nil || !isNetworkAlert(rule.MetricType) {
		return nil
	}
	link, _ := rule.Filters["network_link"].(string)
	if (link != "public" && link != "private") || rule.Operator != ">=" || rule.SustainedMinutes < 1 || rule.SustainedMinutes > 30 {
		return infraerrors.BadRequest("INVALID_NETWORK_ALERT", "Network alerts require a link, >= comparison, and 1-30 sustained minutes")
	}
	if rule.MetricType == "network_unavailable" {
		if rule.Threshold != 1 {
			return infraerrors.BadRequest("INVALID_NETWORK_ALERT", "Network unavailability threshold must be 1")
		}
	} else if rule.Threshold <= 70 || rule.Threshold > 100 {
		return infraerrors.BadRequest("INVALID_NETWORK_ALERT", "Bandwidth threshold must be above 70 and at most 100")
	}
	return nil
}

type networkAlertDecision struct {
	fire, recover bool
	value         float64
}

// Evaluate the contiguous tail of unique samples rather than evaluator ticks.
// Missing intervals break both streaks and never resolve an active alert.
func decideNetworkAlert(rule *OpsAlertRule, samples []OpsNetworkSample, snapshot *OpsNetworkOverview, now time.Time) networkAlertDecision {
	out := networkAlertDecision{}
	link, _ := rule.Filters["network_link"].(string)
	filtered := []OpsNetworkSample{}
	for _, sample := range samples {
		if sample.ID == link {
			filtered = append(filtered, sample)
		}
	}
	if snapshot == nil {
		return out
	}
	for _, sample := range snapshot.Links {
		if sample.ID == link && sample.Status == "disabled" {
			return out
		}
	}
	if now.Sub(snapshot.CollectedAt) > networkStaleAfter {
		if rule.MetricType == "network_unavailable" {
			out.value = 1
			out.fire = now.Sub(snapshot.CollectedAt) >= networkStaleAfter+time.Duration(rule.SustainedMinutes)*time.Minute
		}
		return out
	}
	if len(filtered) == 0 {
		return out
	}
	latest := filtered[len(filtered)-1]
	if latest.Status == "disabled" {
		return out
	}
	direction := "rx"
	if rule.MetricType == "network_tx_utilization_percent" {
		direction = "tx"
	}
	bad, good := 0.0, 0.0
	badTail, goodTail := true, true
	cursor := latest.End
	for i := len(filtered) - 1; i >= 0; i-- {
		sample := filtered[i]
		if sample.Device != latest.Device || sample.End.After(cursor) || cursor.Sub(sample.End) > networkStaleAfter || sample.Status == "disabled" {
			break
		}
		if rule.MetricType == "network_unavailable" {
			unavailable := sample.Status == "down" || sample.Status == "missing" || sample.Status == "error"
			duration := 0.0
			if i > 0 && filtered[i-1].Device == sample.Device {
				duration = sample.End.Sub(filtered[i-1].End).Seconds()
			}
			if duration <= 0 || duration > networkStaleAfter.Seconds() {
				break
			}
			if unavailable && badTail {
				out.value = 1
				previousStatus := filtered[i-1].Status
				if previousStatus == "down" || previousStatus == "missing" || previousStatus == "error" {
					bad += duration
				} else {
					badTail = false
				}
			} else {
				badTail = false
			}
			if sample.Status == "ok" && goodTail {
				good += sample.ValidSeconds
			} else {
				goodTail = false
			}
		} else {
			value := networkUtilization(sample, direction)
			if value == nil {
				break
			}
			if i == len(filtered)-1 {
				out.value = *value
			}
			if compareMetric(*value, rule.Operator, rule.Threshold) && badTail {
				bad += sample.ValidSeconds
			} else {
				badTail = false
			}
			if *value < 70 && goodTail {
				good += sample.ValidSeconds
			} else {
				goodTail = false
			}
			if i > 0 && sample.Start.Sub(filtered[i-1].End).Abs() > time.Millisecond {
				break
			}
		}
		cursor = sample.Start
		if rule.MetricType == "network_unavailable" && i > 0 {
			cursor = filtered[i-1].End
		}
		if !badTail && !goodTail {
			break
		}
	}
	out.fire = bad >= float64(rule.SustainedMinutes*60)-0.001
	out.recover = good >= 300-0.001
	return out
}

func (s *OpsAlertEvaluatorService) evaluateNetworkRule(ctx context.Context, rule *OpsAlertRule, runtimeCfg *OpsAlertRuntimeSettings, now time.Time) {
	if s.opsService == nil || s.opsService.network == nil || validateNetworkAlert(rule) != nil {
		return
	}
	network := s.opsService.network
	settings, err := network.loadSettings(ctx)
	if err != nil || !settings.Enabled || !network.sourceConfigured() {
		return
	}
	link, _ := rule.Filters["network_link"].(string)
	enabled := false
	device := ""
	for _, candidate := range settings.Links {
		if candidate.ID == link {
			enabled = candidate.Enabled
			device = candidate.Device
		}
	}
	if !enabled {
		return
	}
	snapshot, err := network.cache.GetNetworkSnapshot(ctx, network.serverID)
	if err != nil {
		return
	}
	if snapshot == nil {
		snapshot = &OpsNetworkOverview{CollectedAt: network.startedAt}
	} else {
		matches := false
		for _, sample := range snapshot.Links {
			if sample.ID == link && sample.Device == device {
				matches = true
			}
		}
		if !matches {
			return
		}
	}
	window := time.Duration(rule.SustainedMinutes) * time.Minute
	if window < 5*time.Minute {
		window = 5 * time.Minute
	}
	samples, err := network.cache.GetNetworkSamples(ctx, network.serverID, now.Add(-window-2*networkStaleAfter), now)
	if err != nil {
		return
	}
	decision := decideNetworkAlert(rule, samples, snapshot, now)
	active, err := s.opsRepo.GetActiveAlertEvent(ctx, rule.ID)
	if err != nil {
		return
	}
	if active != nil {
		if !decision.recover {
			return
		}
		if err = s.opsRepo.UpdateAlertEventStatus(ctx, active.ID, OpsAlertStatusResolved, &now); err != nil {
			return
		}
		if email, err := s.opsService.GetEmailNotificationConfig(ctx); err == nil && email != nil && email.Alert.IncludeResolvedAlerts {
			resolved := *active
			resolved.Status = OpsAlertStatusResolved
			resolved.ResolvedAt = &now
			resolved.EmailSent = false
			s.maybeSendAlertEmail(ctx, runtimeCfg, rule, &resolved)
		}
		return
	}
	if !decision.fire {
		return
	}
	latest, err := s.opsRepo.GetLatestAlertEvent(ctx, rule.ID)
	if err != nil {
		return
	}
	if latest != nil && now.Sub(latest.FiredAt) < time.Duration(rule.CooldownMinutes)*time.Minute {
		return
	}
	direction := "both"
	if strings.Contains(rule.MetricType, "_rx_") {
		direction = "rx"
	}
	if strings.Contains(rule.MetricType, "_tx_") {
		direction = "tx"
	}
	event := &OpsAlertEvent{RuleID: rule.ID, Severity: rule.Severity, Status: OpsAlertStatusFiring, Title: rule.Name,
		Description: fmt.Sprintf("%s %s %s: %s >= %.2f (current %.2f)", network.serverID, link, device, rule.MetricType, rule.Threshold, decision.value),
		MetricValue: &decision.value, ThresholdValue: &rule.Threshold, Dimensions: map[string]any{"server_id": network.serverID, "network_link": link, "device": device, "direction": direction}, FiredAt: now, CreatedAt: now}
	if runtimeCfg != nil && isOpsAlertSilenced(now, rule, event, runtimeCfg.Silencing) {
		return
	}
	if silent, e := s.opsService.IsAlertSilenced(ctx, rule.ID, "", nil, nil, now); e == nil && silent {
		return
	}
	created, err := s.opsRepo.CreateAlertEvent(ctx, event)
	if err == nil && created != nil {
		s.maybeSendAlertEmail(ctx, runtimeCfg, rule, created)
	}
}
