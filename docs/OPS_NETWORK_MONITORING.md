# Server bandwidth monitoring

This monitor reads host NIC counters. Public IPv4/IPv6 traffic is measured once
at `enp6s0`; it includes every service on the host. Receive and transmit each
have a separate configurable 1000 Mbps capacity. `enp7s0` is the OVH vRack NIC
and remains disabled until private networking is deliberately configured.
Docker bridges, veth pairs, and loopback are not included in these totals.

## Deployment

Deployment requires an explicit production instruction. The application defaults
to an unconfigured collector and does not install or start host services itself.

1. Run backend/default/unit/integration tests, focused race tests, lint, frontend
   tests and build. Keep the current production container running.
2. Sync the reviewed Git commit to `/opt/sub2api-build`. Run
   `bash deploy/network-monitor/install.sh --check` and review the service unit.
3. Run `sudo bash deploy/network-monitor/install.sh --install` on the host.
   The installer pins node_exporter 1.12.1 and verifies its SHA256. Linux exposes
   boot time through the `stat` collector; Sub2API ignores its unrelated counters.
   It listens
   only on the Docker bridge address; the systemd IP filter permits only
   `172.18.0.0/16`. Confirm systemd reports no IP filtering/BPF warnings and an
   external connection to port 9100 is refused before enabling collection.
4. Back up `/opt/sub2api` runtime configuration. Add these environment variables
   to the Sub2API Compose service (putting them only in `.env` is insufficient
   unless Compose references them):

   ```yaml
   OPS_NETWORK_SERVER_ID: sub2api-ovh
   OPS_NETWORK_EXPORTER_URL: http://172.18.0.1:9100/metrics
   ```

5. Build the application image with its Git short hash as tag. The additive
   migration creates minute/hour network tables; do not re-run old migrations.
   Recreate only Sub2API after the image succeeds. Preserve the previous image.
6. In bandwidth settings, confirm public `enp6s0`, receive/transmit 1000 Mbps,
   private disabled. Alert presets are added on demand and start disabled with
   email off; configure recipients through the existing notification settings.

An alternative YAML application configuration is:

```yaml
ops:
  network:
    server_id: sub2api-ovh
    exporter_url: http://172.18.0.1:9100/metrics
```

## Acceptance and operation

- Compare `ip -s -j link show dev enp6s0` byte deltas over the same interval
  with chart totals. Mbps = byte delta * 8 / elapsed seconds / 1,000,000.
  Sample peaks are five-second averages, not instantaneous wire peaks.
- Verify a normal request, container health, collector reachability from Sub2API,
  public isolation, logs, 5-second chart updates and historical gaps. Use normal
  production traffic; do not saturate the 1 Gbps link to test the monitor.
- Raw samples live in Redis for 6 hours; minute history defaults to 30 days and
  hourly history to 180 days. Historical averages use measured time. Partial
  edge buckets and missing samples are marked incomplete.
- Collection failures never participate in gateway admission or scheduling.
  Application restarts establish a fresh counter baseline; stale observations
  cannot resolve bandwidth alerts. Repeated evaluator ticks do not accrue time.
- Rollback: recreate Sub2API with the retained image/config. The additional
  tables may remain. Stop the collector with
  `sudo systemctl disable --now sub2api-node-exporter`. No database restore or
  network-interface changes are needed.
- After verification, remove only this task's test containers and worktrees.
  Retain test reports, shared build caches and production rollback images.
