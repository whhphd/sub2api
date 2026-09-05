#!/usr/bin/env bash
set -euo pipefail

version=1.12.1
expected_sha=b51d8a76aa2a9156a55d501aca6276fae09e262259a5e4e831d2c2222f084e63
archive="node_exporter-${version}.linux-amd64.tar.gz"
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

if [[ ${1:---check} != --install ]]; then
  printf 'node_exporter %s\nSHA256 %s\n' "$version" "$expected_sha"
  printf 'Install: sudo bash %s --install\n' "$0"
  exit 0
fi

[[ $EUID == 0 ]] || { printf 'Run installation as root.\n' >&2; exit 1; }
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || { printf 'Linux amd64 required.\n' >&2; exit 1; }
[[ -d /sys/class/net/br-sub2api ]] || { printf 'Sub2API Docker bridge is missing.\n' >&2; exit 1; }
for executable in curl sha256sum tar systemctl; do command -v "$executable" >/dev/null; done
if systemctl is-active --quiet sub2api-node-exporter; then
  printf 'Collector is already running; inspect its version before replacing it.\n' >&2
  exit 1
fi
if [[ -n $(ss -H -ltn 'sport = :9100') ]]; then
  printf 'Port 9100 is already in use.\n' >&2
  exit 1
fi
tmp_dir=$(mktemp -d)
trap 'rm -r -- "$tmp_dir"' EXIT
curl --fail --location --retry 3 --connect-timeout 15 --max-time 300 \
  "https://github.com/prometheus/node_exporter/releases/download/v${version}/${archive}" -o "$tmp_dir/$archive"
printf '%s  %s\n' "$expected_sha" "$tmp_dir/$archive" | sha256sum --check --status
tar -xzf "$tmp_dir/$archive" -C "$tmp_dir" "node_exporter-${version}.linux-amd64/node_exporter"
install -d -m 0755 /usr/local/lib/sub2api-node-exporter
install -m 0755 "$tmp_dir/node_exporter-${version}.linux-amd64/node_exporter" /usr/local/lib/sub2api-node-exporter/node_exporter
install -m 0644 "$script_dir/sub2api-node-exporter.service" /etc/systemd/system/sub2api-node-exporter.service
systemctl daemon-reload
systemctl enable --now sub2api-node-exporter
systemctl is-active --quiet sub2api-node-exporter
curl --fail --silent --max-time 5 --noproxy '*' http://172.18.0.1:9100/metrics >/dev/null
