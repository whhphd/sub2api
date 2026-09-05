package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const networkFixture = `# TYPE node_boot_time_seconds gauge
node_boot_time_seconds 100
# TYPE node_network_receive_bytes_total counter
node_network_receive_bytes_total{device="enp6s0"} 1000000000
node_network_receive_bytes_total{device="enp7s0"} 0
# TYPE node_network_transmit_bytes_total counter
node_network_transmit_bytes_total{device="enp6s0"} 2000000000
node_network_transmit_bytes_total{device="enp7s0"} 0
# TYPE node_network_up gauge
node_network_up{device="enp6s0"} 1
node_network_up{device="enp7s0"} 0
# TYPE node_network_speed_bytes gauge
node_network_speed_bytes{device="enp6s0"} 125000000
# TYPE node_network_iface_id gauge
node_network_iface_id{device="enp6s0"} 2
`

func TestOpsNetworkParserAndSampleLifecycle(t *testing.T) {
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	base, err := parseNetworkMetrics(strings.NewReader(networkFixture), at)
	require.NoError(t, err)
	require.Equal(t, 1000.0, base.Devices["enp6s0"].SpeedMbps)
	require.False(t, base.Devices["enp7s0"].Up)
	link := DefaultOpsNetworkSettings().Links[0]
	for _, scenario := range []string{"normal", "first", "reboot", "reset", "gap", "duplicate", "down", "missing", "replaced"} {
		t.Run(scenario, func(t *testing.T) {
			current, err := parseNetworkMetrics(strings.NewReader(networkFixture), at.Add(5*time.Second))
			require.NoError(t, err)
			d := current.Devices[link.Device]
			d.RXBytes += 625000000
			d.TXBytes += 312500000
			current.Devices[link.Device] = d
			previous := base
			switch scenario {
			case "first":
				previous = nil
			case "reboot":
				current.Boot++
			case "reset":
				d.RXBytes = 0
				current.Devices[link.Device] = d
			case "gap":
				current.At = at.Add(time.Minute)
			case "duplicate":
				current.At = at
			case "down":
				d.Up = false
				current.Devices[link.Device] = d
			case "missing":
				delete(current.Devices, link.Device)
			case "replaced":
				d.Index++
				current.Devices[link.Device] = d
			}
			sample := calculateNetworkSample(link, previous, current)
			if scenario == "normal" {
				require.Equal(t, "ok", sample.Status)
				require.Equal(t, 1000.0, *sample.RXMbps)
				require.Equal(t, 500.0, *sample.TXMbps)
				require.Equal(t, 100.0, *networkUtilization(sample, "rx"))
				require.Equal(t, 50.0, *networkUtilization(sample, "tx"))
			} else {
				require.Nil(t, sample.RXMbps)
				require.Zero(t, sample.ValidSeconds)
			}
		})
	}
	for _, input := range []string{"bad{", strings.ReplaceAll(networkFixture, "node_boot_time_seconds 100", "node_boot_time_seconds NaN"), strings.ReplaceAll(networkFixture, "node_network_up", "other_up")} {
		_, err := parseNetworkMetrics(strings.NewReader(input), at)
		require.Error(t, err)
	}
}

func networkSample(at time.Time, rate float64) OpsNetworkSample {
	return OpsNetworkSample{OpsNetworkLink: DefaultOpsNetworkSettings().Links[0], Start: at, End: at.Add(5 * time.Second), Status: "ok", RXMbps: &rate, TXMbps: &rate, RXBytes: rate * 1e6 / 8 * 5, TXBytes: rate * 1e6 / 8 * 5, ValidSeconds: 5}
}

func TestOpsNetworkAggregationPreservesBytesAndPeak(t *testing.T) {
	start := time.Date(2026, 9, 5, 12, 0, 58, 0, time.UTC)
	samples := []OpsNetworkSample{networkSample(start, 800), networkSample(start.Add(5*time.Second), 100)}
	buckets := aggregateNetworkSamples(samples, "host", "public", start, start.Add(10*time.Second), 60)
	require.Len(t, buckets, 2)
	require.Equal(t, 2.0, buckets[0].ValidSeconds)
	require.Equal(t, 8.0, buckets[1].ValidSeconds)
	require.InDelta(t, 562500000, buckets[0].RXBytes+buckets[1].RXBytes, 0.01)
	require.Equal(t, 800.0, *buckets[1].RXPeakMbps)
	require.InDelta(t, 362.5, *buckets[1].RXAvgMbps, 0.01)
	require.False(t, buckets[1].Complete)
}

type networkTestCache struct {
	OpsNetworkCache
	snapshot *OpsNetworkOverview
	samples  []OpsNetworkSample
	lease    bool
	commits  int
}

func (c *networkTestCache) RenewNetworkLease(context.Context, string, string) (bool, error) {
	return c.lease, nil
}
func (c *networkTestCache) CommitNetworkSnapshot(_ context.Context, _, _ string, v *OpsNetworkOverview, _ time.Duration) (bool, error) {
	c.snapshot = v
	c.samples = append(c.samples, v.Links...)
	c.commits++
	return true, nil
}
func (c *networkTestCache) GetNetworkSnapshot(context.Context, string) (*OpsNetworkOverview, error) {
	return c.snapshot, nil
}
func (c *networkTestCache) GetNetworkSamples(context.Context, string, time.Time, time.Time) ([]OpsNetworkSample, error) {
	return c.samples, nil
}

type networkTestRepo struct {
	OpsNetworkRepository
	buckets []OpsNetworkBucket
}

func (r *networkTestRepo) UpsertNetworkMinutes(_ context.Context, b []OpsNetworkBucket) error {
	r.buckets = b
	return nil
}
func (r *networkTestRepo) AggregateNetworkHours(context.Context, string, time.Time, time.Time) error {
	return nil
}
func (r *networkTestRepo) GetNetworkBuckets(context.Context, string, string, time.Time, time.Time, int) ([]OpsNetworkBucket, error) {
	return r.buckets, nil
}

func TestOpsNetworkCollectorHonorsLeaseAndErrorStates(t *testing.T) {
	calls := 0
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if fail {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(networkFixture))
	}))
	defer server.Close()
	cache := &networkTestCache{}
	s := NewOpsNetworkService(&networkTestRepo{}, cache, nil, nil, nil)
	s.endpoint = server.URL
	s.serverID = "host"
	s.collect(context.Background())
	require.Zero(t, calls)
	cache.lease = true
	s.collect(context.Background())
	require.Equal(t, 1, calls)
	require.Equal(t, "collecting", cache.snapshot.Links[0].Status)
	require.Equal(t, "disabled", cache.snapshot.Links[1].Status)
	fail = true
	s.collect(context.Background())
	require.Equal(t, "error", cache.snapshot.Status)
	require.Nil(t, s.previous)
	fail = false
	s.collect(context.Background())
	require.Equal(t, "collecting", cache.snapshot.Links[0].Status)
	cache.snapshot.CollectedAt = time.Now().Add(-time.Minute)
	view, err := s.GetOverview(context.Background())
	require.NoError(t, err)
	require.Equal(t, "stale", view.Status)
	require.Nil(t, view.Links[0].RXMbps)
}

func TestOpsNetworkTrendGapAndIndependentScope(t *testing.T) {
	start := time.Now().Truncate(time.Minute).Add(-time.Minute)
	cache := &networkTestCache{samples: []OpsNetworkSample{networkSample(start, 200), networkSample(start.Add(10*time.Second), 600)}}
	s := NewOpsNetworkService(&networkTestRepo{}, cache, nil, nil, nil)
	trend, err := s.GetTrend(context.Background(), "public", start, start.Add(20*time.Second))
	require.NoError(t, err)
	require.Len(t, trend.Points, 4)
	require.Nil(t, trend.Points[1].RXAvgMbps)
	require.Equal(t, 400.0, *trend.Summary.RXAvgMbps)
	require.False(t, trend.Summary.Complete)
	private, err := s.GetTrend(context.Background(), "private", start, start.Add(20*time.Second))
	require.NoError(t, err)
	require.Zero(t, private.Summary.ValidSeconds)
}

func TestOpsNetworkAlertDurationsAndRecovery(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	rule := networkAlertPresets()[0]
	samples := []OpsNetworkSample{}
	for i := 0; i < 36; i++ {
		samples = append(samples, networkSample(start.Add(time.Duration(i)*5*time.Second), 850))
	}
	now := samples[len(samples)-1].End
	snapshot := &OpsNetworkOverview{CollectedAt: now, Links: []OpsNetworkSample{samples[len(samples)-1]}}
	for range 10 {
		require.True(t, decideNetworkAlert(rule, samples, snapshot, now).fire)
	}
	require.False(t, decideNetworkAlert(rule, samples[1:], snapshot, now).fire)
	for i := 0; i < 60; i++ {
		samples = append(samples, networkSample(now.Add(time.Duration(i)*5*time.Second), 650))
	}
	end := samples[len(samples)-1].End
	snapshot.CollectedAt = end
	require.True(t, decideNetworkAlert(rule, samples, snapshot, end).recover)
	samples[len(samples)-2].Status = "error"
	require.False(t, decideNetworkAlert(rule, samples, snapshot, end).recover)
	require.False(t, decideNetworkAlert(rule, samples, snapshot, end.Add(time.Minute)).recover)
	unavailable := networkAlertPresets()[4]
	require.True(t, decideNetworkAlert(unavailable, nil, snapshot, end.Add(2*time.Minute)).fire)
}

func TestOpsNetworkSettingsValidation(t *testing.T) {
	settings := DefaultOpsNetworkSettings()
	require.NoError(t, validateNetworkSettings(settings))
	settings.Links[1].Enabled = true
	settings.Links[1].Device = "enp6s0"
	require.Error(t, validateNetworkSettings(settings))
	settings = DefaultOpsNetworkSettings()
	settings.HourlyRetentionDays = 5
	require.Error(t, validateNetworkSettings(settings))
	require.Error(t, validateNetworkAlert(&OpsAlertRule{MetricType: "network_rx_utilization_percent"}))
}

type networkFailReader struct{}

func (networkFailReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func TestOpsNetworkParserPropagatesReadError(t *testing.T) {
	_, err := parseNetworkMetrics(networkFailReader{}, time.Now())
	require.Error(t, err)
}
