package service

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

func (s *OpsNetworkService) scrape(ctx context.Context) (*networkCounters, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain; version=0.0.4")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("network collector status %d", resp.StatusCode)
	}
	return parseNetworkMetrics(io.LimitReader(resp.Body, 2*1024*1024), time.Now())
}

func parseNetworkMetrics(reader io.Reader, at time.Time) (*networkCounters, error) {
	decoder := expfmt.NewDecoder(reader, expfmt.NewFormat(expfmt.TypeTextPlain))
	out := &networkCounters{At: at, Devices: map[string]OpsNetworkDevice{}}
	seen := map[string]map[string]bool{}
	for {
		var family dto.MetricFamily
		err := decoder.Decode(&family)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		for _, metric := range family.Metric {
			value := metric.GetCounter().GetValue()
			if metric.Gauge != nil {
				value = metric.GetGauge().GetValue()
			}
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				continue
			}
			name := family.GetName()
			if name == "node_boot_time_seconds" {
				out.Boot = value
				continue
			}
			device := ""
			for _, label := range metric.Label {
				if label.GetName() == "device" {
					device = label.GetValue()
				}
			}
			if device == "" {
				continue
			}
			d := out.Devices[device]
			d.Name = device
			switch name {
			case "node_network_receive_bytes_total":
				d.RXBytes = value
			case "node_network_transmit_bytes_total":
				d.TXBytes = value
			case "node_network_up":
				d.Up = value == 1
			case "node_network_speed_bytes":
				d.SpeedMbps = value * 8 / 1e6
			case "node_network_iface_id":
				d.Index = value
			case "node_network_receive_errs_total":
				d.RXErrors = value
			case "node_network_transmit_errs_total":
				d.TXErrors = value
			case "node_network_receive_drop_total":
				d.RXDropped = value
			case "node_network_transmit_drop_total":
				d.TXDropped = value
			default:
				continue
			}
			if seen[device] == nil {
				seen[device] = map[string]bool{}
			}
			seen[device][name] = true
			out.Devices[device] = d
		}
	}
	if out.Boot == 0 {
		return nil, fmt.Errorf("network collector boot time missing")
	}
	for name := range out.Devices {
		if !seen[name]["node_network_receive_bytes_total"] || !seen[name]["node_network_transmit_bytes_total"] || !seen[name]["node_network_up"] {
			delete(out.Devices, name)
		}
	}
	if len(out.Devices) == 0 {
		return nil, fmt.Errorf("network collector counters missing")
	}
	return out, nil
}
