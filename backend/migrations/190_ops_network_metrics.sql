SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS ops_network_metrics_minute (
    server_id VARCHAR(128) NOT NULL,
    link VARCHAR(16) NOT NULL,
    device VARCHAR(64) NOT NULL,
    bucket_start TIMESTAMPTZ NOT NULL,
    rx_bytes DOUBLE PRECISION NOT NULL DEFAULT 0,
    tx_bytes DOUBLE PRECISION NOT NULL DEFAULT 0,
    valid_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    rx_peak_mbps DOUBLE PRECISION,
    tx_peak_mbps DOUBLE PRECISION,
    rx_capacity_mbps DOUBLE PRECISION NOT NULL,
    tx_capacity_mbps DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (server_id, link, device, bucket_start)
);
CREATE INDEX IF NOT EXISTS ops_network_metrics_minute_time ON ops_network_metrics_minute(bucket_start);
CREATE TABLE IF NOT EXISTS ops_network_metrics_hour (LIKE ops_network_metrics_minute INCLUDING ALL);
