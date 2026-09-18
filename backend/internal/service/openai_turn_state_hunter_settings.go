// SPDX-License-Identifier: LGPL-3.0-only
// Hunter policy adapted from KlN klno.12 (2916a74b3). CallAI stores one global
// configuration, not per-account editable copies.
package service

import (
	"fmt"
	"slices"
	"strings"
)

type TurnStateHunterSettings struct {
	Enabled              bool     `json:"enabled"`
	Models               []string `json:"models"`
	ProxyIDs             []int64  `json:"proxy_ids"`
	MaxPerHour           int      `json:"max_per_hour"`
	PerAccountMaxPerHour int      `json:"per_account_max_per_hour"`
	GapSeconds           int      `json:"gap_seconds"`
	LeadMinutes          int      `json:"lead_minutes"`
	RetryMinutes         int      `json:"retry_minutes"`
	IdleMinutes          int      `json:"idle_minutes"`
	ReasoningEffort      string   `json:"reasoning_effort"`
}

func DefaultTurnStateHunterSettings() TurnStateHunterSettings {
	return TurnStateHunterSettings{Models: []string{}, ProxyIDs: []int64{}, MaxPerHour: 300, PerAccountMaxPerHour: 30, GapSeconds: 20, LeadMinutes: 10, RetryMinutes: 10, IdleMinutes: 60, ReasoningEffort: "high"}
}
func (c TurnStateHunterSettings) clone() TurnStateHunterSettings {
	c.Models = append([]string{}, c.Models...)
	c.ProxyIDs = append([]int64{}, c.ProxyIDs...)
	return c
}
func (c TurnStateHunterSettings) validate() error {
	if len(c.Models) > 8 || len(c.ProxyIDs) > 64 {
		return fmt.Errorf("hunter supports at most 8 models and 64 proxies")
	}
	if c.Enabled && (len(c.Models) == 0 || len(c.ProxyIDs) == 0) {
		return fmt.Errorf("hunter requires models and proxies")
	}
	models := map[string]bool{}
	ids := map[int64]bool{}
	for _, m := range c.Models {
		if turnStateModel(m) == "" || m != turnStateModel(m) || models[m] {
			return fmt.Errorf("hunter models must be unique normalized model names")
		}
		models[m] = true
	}
	for _, id := range c.ProxyIDs {
		if id <= 0 || ids[id] {
			return fmt.Errorf("hunter proxy IDs must be unique positive integers")
		}
		ids[id] = true
	}
	if c.MaxPerHour < 1 || c.MaxPerHour > 600 || c.PerAccountMaxPerHour < 1 || c.PerAccountMaxPerHour > 600 {
		return fmt.Errorf("hunter hourly limits must be 1-600")
	}
	if c.GapSeconds < 1 || c.GapSeconds > 600 || c.LeadMinutes < 1 || c.LeadMinutes > 55 || c.RetryMinutes < 1 || c.RetryMinutes > 1440 || (c.IdleMinutes != -1 && (c.IdleMinutes < 1 || c.IdleMinutes > 1440)) {
		return fmt.Errorf("invalid hunter timing settings")
	}
	if !slices.Contains([]string{"minimal", "low", "medium", "high", "xhigh"}, c.ReasoningEffort) {
		return fmt.Errorf("invalid hunter reasoning effort")
	}
	return nil
}
func (c TurnStateHunterSettings) hunts(model string) bool {
	return c.Enabled && slices.Contains(c.Models, strings.ToLower(strings.TrimSpace(model)))
}

// Managed runtime state is never accepted from account create/edit/import payloads.
const CodexTurnStateHuntKey = "openai_turn_state_hunt"

func stripTurnStateRuntimeExtra(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	out := make(map[string]any, len(extra))
	for k, v := range extra {
		if k != CodexTurnStatePoolKey && k != CodexTurnStateObservationKey && k != CodexTurnStateSummaryKey && k != CodexTurnStateHuntKey {
			out[k] = v
		}
	}
	return out
}
