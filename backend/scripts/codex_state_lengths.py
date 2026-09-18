#!/usr/bin/env python3
"""Aggregate diagnostic JSON/console logs from stdin; never emit token values.

292 and 312 below are state LENGTHS, separate from HTTP status codes. HTTP
header records are joined to body-end records by attempt; guard/relay events
are not counted again. Account/model correlations are observational only.
"""
import collections
import json
import re
import sys


def length_key(event, prefix):
    value = event.get(prefix + "_length")
    if isinstance(value, int) and not isinstance(value, bool) and value >= 0:
        return str(value)
    return "unobserved"


def safe_ref(value):
    return value if isinstance(value, str) and re.fullmatch(r"[0-9a-f]{24}", value) else "unknown"


def safe_model(value):
    if isinstance(value, str) and (re.fullmatch(r"gpt-[A-Za-z0-9._-]{1,76}", value) or re.fullmatch(r"ref:[0-9a-f]{24}", value)):
        return value
    return "unknown"


def aggregate(lines):
    headers, ends = {}, {}
    sends = set()
    outcomes = collections.Counter()
    ws = collections.Counter()
    suppressed = malformed = duplicate = incomplete = 0
    first = last = None
    modes = collections.Counter()
    for line in lines:
        if "codex_state_diagnostic" not in line:
            continue
        try:
            event = json.loads(line[line.index("{"):])
        except (ValueError, TypeError):
            malformed += 1
            continue
        if event.get("component") != "service.codex_state_diagnostics":
            continue
        dropped = event.get("suppressed_events", 0)
        if isinstance(dropped, int) and dropped > 0:
            suppressed += dropped
        timestamp = event.get("time") or line.split("\t", 1)[0]
        if isinstance(timestamp, str) and re.fullmatch(r"[0-9T:.+Z-]+", timestamp):
            first = first or timestamp
            last = timestamp
        modes[str((event.get("all_accounts"), event.get("sample_percent"), event.get("full_capture")))] += 1
        kind = event.get("event")
        attempt = event.get("attempt")
        if kind in ("http_send", "http_headers", "http_body_end", "http_transport_end"):
            if not isinstance(attempt, int):
                incomplete += 1
                continue
            if kind == "http_send":
                sends.add(attempt)
            elif kind == "http_headers":
                duplicate += attempt in headers
                # Keep only allowed fields, not arbitrary source data.
                status = event.get("upstream_status")
                headers[attempt] = {
                    "account": safe_ref(event.get("account_ref")),
                    "model": safe_model(event.get("model")),
                    "sent_length": length_key(event, "sent_state"),
                    "received_length": length_key(event, "received_state"),
                    "http_status": str(status) if isinstance(status, int) and 100 <= status <= 599 else "unknown",
                }
            else:
                outcome = event.get("outcome")
                outcome = outcome if outcome in ("eof", "closed", "read_error", "canceled", "timeout", "transport_error") else "unknown"
                error = event.get("error_class")
                error = error if error in ("none", "quota_exhausted", "overload", "rate_limited", "rate_limited_unclassified", "authentication_or_access", "upstream_server_error", "upstream_client_error", "other_upstream_error") else "unknown"
                ends[attempt] = (outcome, error, bool(event.get("observation_truncated")))
        elif kind == "ws_frame":
            boundary = event.get("boundary")
            boundary = boundary if boundary in ("upstream_receive", "upstream_send_attempt", "downstream_boundary") else "unknown"
            if event.get("frame_event") == "response.create" and event.get("outbound") is True:
                ws[(boundary, "request", length_key(event, "sent_state"))] += 1
            elif event.get("frame_event") == "response.metadata" and event.get("outbound") is False:
                ws[(boundary, "response", length_key(event, "received_state"))] += 1
    statuses, sent, received = (collections.Counter() for _ in range(3))
    groups = collections.Counter()
    truncated = 0
    for attempt, row in headers.items():
        statuses[row["http_status"]] += 1
        sent[row["sent_length"]] += 1
        received[row["received_length"]] += 1
        outcome, error, cut = ends.get(attempt, ("pending_or_unobserved", "unknown", False))
        outcomes[outcome] += 1
        truncated += cut
        groups[(row["account"], row["model"], row["sent_length"], row["received_length"], row["http_status"], error, outcome)] += 1
    return {
        "schema": 1, "measurement": "state_length_not_http_status",
        "first_diagnostic_at": first, "last_diagnostic_at": last,
        "http_header_observations": len(headers), "http_send_observations": len(sends),
        "upstream_http_status": dict(statuses),
        "sent_state_lengths": dict(sent), "received_state_lengths": dict(received),
        "state_length_292": {"sent": sent["292"], "received": received["292"]},
        "state_length_312": {"sent": sent["312"], "received": received["312"]},
        "accounts_with_http_headers": len({r["account"] for r in headers.values()} - {"unknown"}),
        "outcomes": dict(outcomes), "body_observation_truncated": truncated,
        "suppressed_events": suppressed, "malformed_diagnostic_lines": malformed,
        "duplicate_header_records": duplicate, "events_missing_attempt": incomplete,
        "headers_missing_send": len(set(headers) - sends),
        "sends_without_headers": len(sends - set(headers)), "observed_modes": dict(modes),
        "ws_state_lengths": [{"boundary": k[0], "direction": k[1], "length": k[2], "count": n} for k, n in sorted(ws.items())],
        "by_account_model_length_status": [dict(zip(("account_ref", "model", "sent_length", "received_length", "http_status", "error_class", "outcome"), k), count=n) for k, n in sorted(groups.items())],
        "limitations": "Attempts include retries; EOF is not proof of client success. Unknown/truncated data cannot establish error absence. Lengths do not establish model quality. Use one process lifetime per report.",
    }


if __name__ == "__main__":
    json.dump(aggregate(sys.stdin), sys.stdout, indent=2)
    sys.stdout.write("\n")
