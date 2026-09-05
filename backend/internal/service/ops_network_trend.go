package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

func networkBucketStats(b *OpsNetworkBucket, expected float64) {
	if b.ValidSeconds > 0 {
		rx, tx := b.RXBytes*8/b.ValidSeconds/1e6, b.TXBytes*8/b.ValidSeconds/1e6
		b.RXAvgMbps, b.TXAvgMbps = &rx, &tx
	}
	b.Complete = expected > 0 && math.Abs(b.ValidSeconds-expected) < 0.05
}

func maxNetworkValue(a, b *float64) *float64 {
	if b != nil && (a == nil || *b > *a) {
		v := *b
		return &v
	}
	return a
}

// Split measured intervals at bucket boundaries to preserve byte totals.
func aggregateNetworkSamples(samples []OpsNetworkSample, server, link string, start, end time.Time, seconds int) []OpsNetworkBucket {
	buckets := map[string]*OpsNetworkBucket{}
	step := time.Duration(seconds) * time.Second
	for _, sample := range samples {
		if sample.Status != "ok" || sample.ValidSeconds <= 0 || (link != "" && sample.ID != link) {
			continue
		}
		lo, hi := sample.Start, sample.End
		if lo.Before(start) {
			lo = start
		}
		if hi.After(end) {
			hi = end
		}
		for at := lo; at.Before(hi); {
			bucketStart := at.Truncate(step)
			next := bucketStart.Add(step)
			if next.After(hi) {
				next = hi
			}
			duration := next.Sub(at).Seconds()
			key := fmt.Sprintf("%s/%s/%d", sample.ID, sample.Device, bucketStart.UnixNano())
			b := buckets[key]
			if b == nil {
				b = &OpsNetworkBucket{ServerID: server, Link: sample.ID, Device: sample.Device, BucketStart: bucketStart, BucketSeconds: seconds}
				buckets[key] = b
			}
			b.ValidSeconds += duration
			b.RXBytes += sample.RXBytes * duration / sample.ValidSeconds
			b.TXBytes += sample.TXBytes * duration / sample.ValidSeconds
			b.RXCapacityMbps += sample.RXCapacityMbps * duration
			b.TXCapacityMbps += sample.TXCapacityMbps * duration
			b.RXPeakMbps = maxNetworkValue(b.RXPeakMbps, sample.RXMbps)
			b.TXPeakMbps = maxNetworkValue(b.TXPeakMbps, sample.TXMbps)
			at = next
		}
	}
	out := make([]OpsNetworkBucket, 0, len(buckets))
	for _, b := range buckets {
		b.RXCapacityMbps /= b.ValidSeconds
		b.TXCapacityMbps /= b.ValidSeconds
		networkBucketStats(b, float64(seconds))
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BucketStart.Before(out[j].BucketStart) })
	return out
}

func (s *OpsNetworkService) GetTrend(ctx context.Context, link string, start, end time.Time) (*OpsNetworkTrend, error) {
	if link != "public" && link != "private" {
		return nil, fmt.Errorf("invalid network link")
	}
	if !start.Before(end) || end.Sub(start) > 180*24*time.Hour {
		return nil, fmt.Errorf("network range must be within 180 days")
	}
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	seconds := 3600
	if end.Sub(start) <= 24*time.Hour {
		seconds = 60
	}
	if end.Sub(start) <= time.Hour && !start.Before(time.Now().Add(-time.Duration(settings.RawRetentionHours)*time.Hour)) {
		seconds = 5
	}
	var rows []OpsNetworkBucket
	step := time.Duration(seconds) * time.Second
	if seconds == 5 {
		samples, e := s.cache.GetNetworkSamples(ctx, s.serverID, start.Add(-networkStaleAfter), end)
		if e != nil {
			return nil, e
		}
		rows = aggregateNetworkSamples(samples, s.serverID, link, start, end, seconds)
	} else {
		rows, err = s.repo.GetNetworkBuckets(ctx, s.serverID, link, start.Truncate(step), end, seconds)
		if err != nil {
			return nil, err
		}
	}
	out := &OpsNetworkTrend{ServerID: s.serverID, Link: link, Start: start, End: end, BucketSeconds: seconds, Points: []OpsNetworkBucket{}}
	byTime := map[int64]OpsNetworkBucket{}
	for _, row := range rows {
		key := row.BucketStart.Unix()
		b := byTime[key]
		b.ServerID = s.serverID
		b.Link = link
		b.BucketStart = row.BucketStart
		b.BucketSeconds = seconds
		b.RXBytes += row.RXBytes
		b.TXBytes += row.TXBytes
		b.ValidSeconds += row.ValidSeconds
		b.RXCapacityMbps += row.RXCapacityMbps * row.ValidSeconds
		b.TXCapacityMbps += row.TXCapacityMbps * row.ValidSeconds
		b.RXPeakMbps = maxNetworkValue(b.RXPeakMbps, row.RXPeakMbps)
		b.TXPeakMbps = maxNetworkValue(b.TXPeakMbps, row.TXPeakMbps)
		byTime[key] = b
	}
	for at := start.Truncate(step); at.Before(end); at = at.Add(step) {
		b, ok := byTime[at.Unix()]
		if !ok {
			b = OpsNetworkBucket{ServerID: s.serverID, Link: link, BucketStart: at, BucketSeconds: seconds}
		}
		lo, hi := at, at.Add(step)
		if lo.Before(start) {
			lo = start
		}
		if hi.After(end) {
			hi = end
		}
		expected := hi.Sub(lo).Seconds()
		if b.ValidSeconds > 0 {
			b.RXCapacityMbps /= b.ValidSeconds
			b.TXCapacityMbps /= b.ValidSeconds
		}
		if seconds > 5 && expected < float64(seconds) {
			fraction := expected / float64(seconds)
			b.RXBytes *= fraction
			b.TXBytes *= fraction
			b.ValidSeconds *= fraction
		}
		networkBucketStats(&b, expected)
		if seconds > 5 && expected < float64(seconds) {
			b.Complete = false
		}
		out.Points = append(out.Points, b)
		out.Summary.RXBytes += b.RXBytes
		out.Summary.TXBytes += b.TXBytes
		out.Summary.ValidSeconds += b.ValidSeconds
		out.Summary.RXPeakMbps = maxNetworkValue(out.Summary.RXPeakMbps, b.RXPeakMbps)
		out.Summary.TXPeakMbps = maxNetworkValue(out.Summary.TXPeakMbps, b.TXPeakMbps)
	}
	networkBucketStats(&out.Summary, end.Sub(start).Seconds())
	for _, b := range out.Points {
		if !b.Complete {
			out.Summary.Complete = false
		}
	}
	return out, nil
}

func networkUtilization(sample OpsNetworkSample, direction string) *float64 {
	rate, capacity := sample.RXMbps, sample.RXCapacityMbps
	if direction == "tx" {
		rate, capacity = sample.TXMbps, sample.TXCapacityMbps
	}
	if rate == nil || capacity <= 0 || sample.Status != "ok" {
		return nil
	}
	v := *rate / capacity * 100
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}
