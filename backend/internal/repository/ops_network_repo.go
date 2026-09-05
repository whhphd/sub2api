package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *opsRepository) UpsertNetworkMinutes(ctx context.Context, buckets []service.OpsNetworkBucket) error {
	if len(buckets) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO ops_network_metrics_minute
 (server_id,link,device,bucket_start,rx_bytes,tx_bytes,valid_seconds,rx_peak_mbps,tx_peak_mbps,rx_capacity_mbps,tx_capacity_mbps)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
 ON CONFLICT(server_id,link,device,bucket_start) DO UPDATE SET
 rx_bytes=EXCLUDED.rx_bytes,tx_bytes=EXCLUDED.tx_bytes,valid_seconds=EXCLUDED.valid_seconds,
 rx_peak_mbps=EXCLUDED.rx_peak_mbps,tx_peak_mbps=EXCLUDED.tx_peak_mbps,
 rx_capacity_mbps=EXCLUDED.rx_capacity_mbps,tx_capacity_mbps=EXCLUDED.tx_capacity_mbps
 WHERE ops_network_metrics_minute.valid_seconds <= EXCLUDED.valid_seconds`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, b := range buckets {
		if _, err = stmt.ExecContext(ctx, b.ServerID, b.Link, b.Device, b.BucketStart, b.RXBytes, b.TXBytes, b.ValidSeconds, b.RXPeakMbps, b.TXPeakMbps, b.RXCapacityMbps, b.TXCapacityMbps); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *opsRepository) AggregateNetworkHours(ctx context.Context, server string, start, end time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO ops_network_metrics_hour
 (server_id,link,device,bucket_start,rx_bytes,tx_bytes,valid_seconds,rx_peak_mbps,tx_peak_mbps,rx_capacity_mbps,tx_capacity_mbps)
 SELECT server_id,link,device,date_trunc('hour',bucket_start),sum(rx_bytes),sum(tx_bytes),sum(valid_seconds),
 max(rx_peak_mbps),max(tx_peak_mbps),
 COALESCE(sum(rx_capacity_mbps*valid_seconds)/NULLIF(sum(valid_seconds),0),max(rx_capacity_mbps)),
 COALESCE(sum(tx_capacity_mbps*valid_seconds)/NULLIF(sum(valid_seconds),0),max(tx_capacity_mbps))
 FROM ops_network_metrics_minute WHERE server_id=$1 AND bucket_start >= $2 AND bucket_start < $3
 GROUP BY server_id,link,device,date_trunc('hour',bucket_start)
 ON CONFLICT(server_id,link,device,bucket_start) DO UPDATE SET
 rx_bytes=EXCLUDED.rx_bytes,tx_bytes=EXCLUDED.tx_bytes,valid_seconds=EXCLUDED.valid_seconds,
 rx_peak_mbps=EXCLUDED.rx_peak_mbps,tx_peak_mbps=EXCLUDED.tx_peak_mbps,
 rx_capacity_mbps=EXCLUDED.rx_capacity_mbps,tx_capacity_mbps=EXCLUDED.tx_capacity_mbps`, server, start.Truncate(time.Hour), end)
	return err
}

func (r *opsRepository) GetNetworkBuckets(ctx context.Context, server, link string, start, end time.Time, seconds int) ([]service.OpsNetworkBucket, error) {
	table := "ops_network_metrics_minute"
	if seconds == 3600 {
		table = "ops_network_metrics_hour"
	} else if seconds != 60 {
		return nil, fmt.Errorf("invalid network bucket")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT bucket_start,sum(rx_bytes),sum(tx_bytes),sum(valid_seconds),max(rx_peak_mbps),max(tx_peak_mbps),
 COALESCE(sum(rx_capacity_mbps*valid_seconds)/NULLIF(sum(valid_seconds),0),max(rx_capacity_mbps)),
 COALESCE(sum(tx_capacity_mbps*valid_seconds)/NULLIF(sum(valid_seconds),0),max(tx_capacity_mbps))
 FROM `+table+` WHERE server_id=$1 AND link=$2 AND bucket_start >= $3 AND bucket_start < $4 GROUP BY bucket_start ORDER BY bucket_start`, server, link, start, end)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.OpsNetworkBucket{}
	for rows.Next() {
		b := service.OpsNetworkBucket{ServerID: server, Link: link, BucketSeconds: seconds}
		if err := rows.Scan(&b.BucketStart, &b.RXBytes, &b.TXBytes, &b.ValidSeconds, &b.RXPeakMbps, &b.TXPeakMbps, &b.RXCapacityMbps, &b.TXCapacityMbps); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *opsRepository) CleanupNetworkMetrics(ctx context.Context, minute, hour time.Time) error {
	for _, target := range []struct {
		table  string
		cutoff time.Time
	}{{"ops_network_metrics_minute", minute}, {"ops_network_metrics_hour", hour}} {
		for {
			res, err := r.db.ExecContext(ctx, `DELETE FROM `+target.table+` WHERE ctid IN (SELECT ctid FROM `+target.table+` WHERE bucket_start < $1 LIMIT 5000)`, target.cutoff)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n < 5000 {
				break
			}
		}
	}
	return nil
}
