package service

import (
	"context"
	"time"
)

const OpsNetworkSettingsKey = "ops_network_settings"

type OpsNetworkLink struct {
	ID             string  `json:"id"`
	Enabled        bool    `json:"enabled"`
	Device         string  `json:"device"`
	RXCapacityMbps float64 `json:"rx_capacity_mbps"`
	TXCapacityMbps float64 `json:"tx_capacity_mbps"`
}

type OpsNetworkSettings struct {
	Enabled             bool             `json:"enabled"`
	Links               []OpsNetworkLink `json:"links"`
	RawRetentionHours   int              `json:"raw_retention_hours"`
	MinuteRetentionDays int              `json:"minute_retention_days"`
	HourlyRetentionDays int              `json:"hourly_retention_days"`
}

func DefaultOpsNetworkSettings() OpsNetworkSettings {
	return OpsNetworkSettings{Enabled: true, RawRetentionHours: 6, MinuteRetentionDays: 30, HourlyRetentionDays: 180,
		Links: []OpsNetworkLink{{ID: "public", Enabled: true, Device: "enp6s0", RXCapacityMbps: 1000, TXCapacityMbps: 1000},
			{ID: "private", Device: "enp7s0", RXCapacityMbps: 1000, TXCapacityMbps: 1000}}}
}

type OpsNetworkDevice struct {
	Name      string  `json:"name"`
	Up        bool    `json:"up"`
	SpeedMbps float64 `json:"speed_mbps"`
	Index     float64 `json:"index"`
	RXBytes   float64 `json:"-"`
	TXBytes   float64 `json:"-"`
	RXErrors  float64 `json:"rx_errors"`
	TXErrors  float64 `json:"tx_errors"`
	RXDropped float64 `json:"rx_dropped"`
	TXDropped float64 `json:"tx_dropped"`
}

type OpsNetworkSample struct {
	OpsNetworkLink
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	Status       string    `json:"status"`
	RXMbps       *float64  `json:"rx_mbps"`
	TXMbps       *float64  `json:"tx_mbps"`
	RXBytes      float64   `json:"rx_bytes"`
	TXBytes      float64   `json:"tx_bytes"`
	ValidSeconds float64   `json:"valid_seconds"`
}

type OpsNetworkOverview struct {
	ServerID    string             `json:"server_id"`
	CollectedAt time.Time          `json:"collected_at"`
	Status      string             `json:"status"`
	Devices     []OpsNetworkDevice `json:"devices"`
	Links       []OpsNetworkSample `json:"links"`
}

type OpsNetworkBucket struct {
	ServerID       string    `json:"server_id"`
	Link           string    `json:"link"`
	Device         string    `json:"device"`
	BucketStart    time.Time `json:"bucket_start"`
	BucketSeconds  int       `json:"bucket_seconds"`
	RXBytes        float64   `json:"rx_bytes"`
	TXBytes        float64   `json:"tx_bytes"`
	ValidSeconds   float64   `json:"valid_seconds"`
	RXAvgMbps      *float64  `json:"rx_avg_mbps"`
	TXAvgMbps      *float64  `json:"tx_avg_mbps"`
	RXPeakMbps     *float64  `json:"rx_peak_mbps"`
	TXPeakMbps     *float64  `json:"tx_peak_mbps"`
	RXCapacityMbps float64   `json:"rx_capacity_mbps"`
	TXCapacityMbps float64   `json:"tx_capacity_mbps"`
	Complete       bool      `json:"complete"`
}

type OpsNetworkTrend struct {
	ServerID      string             `json:"server_id"`
	Link          string             `json:"link"`
	Start         time.Time          `json:"start"`
	End           time.Time          `json:"end"`
	BucketSeconds int                `json:"bucket_seconds"`
	Points        []OpsNetworkBucket `json:"points"`
	Summary       OpsNetworkBucket   `json:"summary"`
}

type OpsNetworkSettingsResponse struct {
	Settings         OpsNetworkSettings `json:"settings"`
	ServerID         string             `json:"server_id"`
	SourceConfigured bool               `json:"source_configured"`
	Devices          []OpsNetworkDevice `json:"devices"`
	AlertPresets     []*OpsAlertRule    `json:"alert_presets"`
}

type OpsNetworkRepository interface {
	UpsertNetworkMinutes(context.Context, []OpsNetworkBucket) error
	AggregateNetworkHours(context.Context, string, time.Time, time.Time) error
	GetNetworkBuckets(context.Context, string, string, time.Time, time.Time, int) ([]OpsNetworkBucket, error)
	CleanupNetworkMetrics(context.Context, time.Time, time.Time) error
}

type OpsNetworkCache interface {
	RenewNetworkLease(context.Context, string, string) (bool, error)
	ReleaseNetworkLease(context.Context, string, string) error
	CommitNetworkSnapshot(context.Context, string, string, *OpsNetworkOverview, time.Duration) (bool, error)
	GetNetworkSnapshot(context.Context, string) (*OpsNetworkOverview, error)
	GetNetworkSamples(context.Context, string, time.Time, time.Time) ([]OpsNetworkSample, error)
}
