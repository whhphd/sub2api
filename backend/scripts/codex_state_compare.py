#!/usr/bin/env python3
"""Bounded, opt-in paired Responses protocol probes. No state injection/renewal.

Uses dedicated credentials from environment variables. Reports metadata and
objective grades only; never saves model output, credentials, or state tokens.
This is a protocol harness, not an emulation of the official Codex client.
"""
import argparse
import hashlib
import json
import os
import socket
import statistics
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

MAX_BYTES = 2 * 1024 * 1024
MAX_LINE = 64 * 1024
MAX_OUTPUT_ITEMS = 128


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def error_class(status, value):
    if not isinstance(value, dict):
        raise ValueError("invalid response envelope")
    response = value.get("response") or {}
    if not isinstance(response, dict):
        raise ValueError("invalid response object")
    error = value.get("error") or response.get("error") or {}
    code = error.get("code") if isinstance(error, dict) else ""
    if code in ("usage_limit_reached", "insufficient_quota", "quota_exceeded"):
        return "quota_exhausted"
    if code in ("overloaded", "overloaded_error", "server_overloaded", "server_is_overloaded", "slow_down"):
        return "overload"
    if code in ("rate_limit_exceeded", "rate_limit_error"):
        return "rate_limited"
    if status == 429:
        return "rate_limited_unclassified"
    if status >= 500:
        return "upstream_server_error"
    if status >= 400 or code:
        return "other_error"
    return "none"


def validate(config):
    if not isinstance(config.get("model"), str) or not config["model"].strip():
        raise ValueError("model is required")
    if config.get("reasoning_effort") not in ("low", "medium", "high", "xhigh", "max", "ultra"):
        raise ValueError("reasoning_effort must be explicit")
    if len(config.get("arms", [])) != 2:
        raise ValueError("exactly two arms are required")
    for arm in config["arms"]:
        url = urllib.parse.urlsplit(arm["base_url"])
        local = url.hostname in ("127.0.0.1", "localhost", "::1")
        if url.scheme != "https" and not (url.scheme == "http" and local):
            raise ValueError("HTTPS is required except on loopback")
        if not url.hostname or url.username or url.password or url.query or url.fragment:
            raise ValueError("base_url cannot contain credentials, query, or fragment")
        for key in ("credential_env", "chatgpt_account_id_env", "proxy_env"):
            if key in arm and (not isinstance(arm[key], str) or not arm[key].isidentifier()):
                raise ValueError("credential fields must name environment variables")
        if not arm.get("credential_env"):
            raise ValueError("credential_env is required")


def cases():
    # Fixed content, deterministic graders. Model code is never executed.
    return [
        {"id": "arithmetic", "prompt": 'Compute 17*19-23. Reply only with JSON: {"answer": integer}.', "answer": 300},
        {"id": "logic", "prompt": 'A precedes B. C follows B. D precedes A. Reply only with JSON containing the ordering as an array: {"answer": [...]}.', "answer": ["D", "A", "B", "C"]},
        {"id": "context", "prompt": "Remember marker R728 has value violet-cedar.\n" + "Filler entry, unrelated to marker R728.\n" * 256 + '\nWhat is R728? Reply only with JSON: {"answer": string}.', "answer": "violet-cedar"},
        {"id": "tool_continuation", "prompt": 'Call lookup_marker with key R728, then reply only with JSON {"answer": the returned value}.', "answer": "violet-cedar", "tool": True},
    ]


def grade(case, output):
    try:
        text = "".join(part.get("text", "") for item in output if item.get("type") == "message"
                       for part in item.get("content", []) if part.get("type") == "output_text")
        return json.loads(text).get("answer") == case["answer"]
    except (ValueError, AttributeError, TypeError):
        return False


def consume(response, start):
    status = response.status
    state = response.headers.get("x-codex-turn-state", "")
    meta = {"http_status": status, "state_received": bool(state),
            "alternate_state_received": bool(response.headers.get("current_turn_state")),
            "headers_ms": round((time.monotonic() - start) * 1000, 1),
            "first_output_ms": None, "terminal": "missing", "error_class": "none"}
    output = []
    if "text/event-stream" not in response.headers.get("Content-Type", ""):
        body = response.read(MAX_BYTES + 1)
        if len(body) > MAX_BYTES:
            raise ValueError("response limit exceeded")
        try:
            data = json.loads(body)
        except ValueError:
            data = {}
        meta["error_class"] = error_class(status, data)
        # Non-SSE success is not treated as a passed streaming contract.
        meta["terminal"] = "non_stream_response"
        return output, state, meta
    total = 0
    data_lines = []
    data_size = 0
    while True:
        # Socket timeout bounds inactivity; also cap total reading time.
        if time.monotonic() - start > 120:
            raise TimeoutError("total response deadline")
        line = response.readline(MAX_LINE + 1)
        total += len(line)
        if len(line) > MAX_LINE or total > MAX_BYTES:
            raise ValueError("response limit exceeded")
        if line and line.strip():
            if line.startswith(b"data:"):
                data_lines.append(line[5:].lstrip().rstrip(b"\r\n"))
                data_size += len(data_lines[-1])
                if data_size > MAX_LINE:
                    raise ValueError("SSE event limit exceeded")
            continue
        if data_lines:
            raw = b"\n".join(data_lines)
            data_lines, data_size = [], 0
            if raw != b"[DONE]":
                data = json.loads(raw)
                if not isinstance(data, dict):
                    raise ValueError("invalid SSE event")
                event = data.get("type")
                if event in ("response.output_text.delta", "response.function_call_arguments.delta", "response.reasoning_summary_text.delta") and meta["first_output_ms"] is None:
                    meta["first_output_ms"] = round((time.monotonic() - start) * 1000, 1)
                if event in ("response.completed", "response.failed", "response.incomplete", "error"):
                    meta["terminal"] = event
                    meta["error_class"] = error_class(status, data)
                    if event in ("error", "response.failed") and meta["error_class"] == "none":
                        meta["error_class"] = "other_error"
                    result = data.get("response") or {}
                    if not isinstance(result, dict):
                        raise ValueError("invalid response object")
                    output = result.get("output") or []
                    if not isinstance(output, list) or len(output) > MAX_OUTPUT_ITEMS or any(not isinstance(item, dict) for item in output):
                        raise ValueError("output item limit exceeded")
                    usage = result.get("usage") or {}
                    if not isinstance(usage, dict):
                        raise ValueError("invalid usage object")
                    meta["usage"] = {k: v for k, v in usage.items() if k in ("input_tokens", "output_tokens", "total_tokens") and isinstance(v, int)}
                    return output, state, meta
        if not line:
            return output, state, meta


def request(arm, payload, session, state=""):
    secret = os.environ[arm["credential_env"]]
    headers = {"Authorization": "Bearer " + secret, "Content-Type": "application/json",
               "Accept": "text/event-stream", "session_id": session,
               "User-Agent": "callai-state-probe/1"}
    if arm.get("chatgpt_account_id_env"):
        headers["ChatGPT-Account-ID"] = os.environ[arm["chatgpt_account_id_env"]]
    if state:
        headers["x-codex-turn-state"] = state
    proxy = os.environ.get(arm.get("proxy_env", ""), "")
    if arm.get("proxy_env") and not proxy:
        raise ValueError("configured proxy is missing")
    opener = urllib.request.build_opener(NoRedirect(), urllib.request.ProxyHandler({"http": proxy, "https": proxy} if proxy else {}))
    req = urllib.request.Request(arm["base_url"].rstrip("/") + "/responses",
                                 data=json.dumps(payload).encode(), headers=headers)
    start = time.monotonic()
    try:
        response = opener.open(req, timeout=30)
    except urllib.error.HTTPError as error:
        response = error  # Keep 292/429/503 as observed status, never rewrite them.
    with response:
        output, returned_state, meta = consume(response, start)
    meta["elapsed_ms"] = round((time.monotonic() - start) * 1000, 1)
    meta["state_sent"] = bool(state)
    return output, returned_state, meta


def probe(config, arm, case):
    payload = {"model": config["model"], "reasoning": {"effort": config["reasoning_effort"]},
               "instructions": "Follow the user task. Return the requested JSON without markdown.",
               "input": [{"role": "user", "content": case["prompt"]}], "store": False, "stream": True,
               "include": ["reasoning.encrypted_content"]}
    if case.get("tool"):
        payload["tools"] = [{"type": "function", "name": "lookup_marker", "description": "Read a fixed test marker.",
                             "parameters": {"type": "object", "properties": {"key": {"type": "string"}}, "required": ["key"], "additionalProperties": False}, "strict": True}]
    session = str(uuid.uuid4())
    observations = []
    state = ""  # A fresh turn NEVER inherits another case's state.
    tool_used = False
    try:
        for step in range(2):
            output, received, meta = request(arm, payload, session, state)
            observations.append(meta)
            if not state:
                state = received  # Same-turn first state wins, as in Codex.
            if meta["terminal"] != "response.completed":
                return {"passed": False, "requests": observations}
            calls = [item for item in output if item.get("type") == "function_call"]
            if not calls:
                return {"passed": grade(case, output) and (tool_used or not case.get("tool")), "requests": observations}
            if step or not case.get("tool") or len(calls) != 1:
                break
            call = calls[0]
            if call.get("name") != "lookup_marker" or json.loads(call.get("arguments", "{}")) != {"key": "R728"}:
                break
            tool_used = True
            payload["input"].extend(output)
            payload["input"].append({"type": "function_call_output", "call_id": call["call_id"], "output": '{"value":"violet-cedar"}'})
    except (urllib.error.URLError, socket.timeout, TimeoutError, ValueError, KeyError, TypeError, OSError) as error:
        # Exception text may include URL credentials or upstream payloads.
        return {"passed": False, "requests": observations, "local_error": type(error).__name__}
    return {"passed": False, "requests": observations}


def summarize(results):
    summary = {}
    for arm in ("A", "B"):
        rows = [row for row in results if row["arm"] == arm]
        requests = [r for row in rows for r in row["requests"]]
        times = [r["first_output_ms"] for r in requests if r.get("first_output_ms") is not None]
        summary[arm] = {"cases": len(rows), "passed": sum(row["passed"] for row in rows),
                        "http_requests_observed": len(requests),
                        "http_292": sum(r["http_status"] == 292 for r in requests),
                        "http_429": sum(r["http_status"] == 429 for r in requests),
                        "overload": sum(r["error_class"] == "overload" for r in requests),
                        "median_first_output_ms": statistics.median(times) if times else None}
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", type=Path)
    parser.add_argument("--run", action="store_true", help="Explicitly send paid/usage-consuming upstream requests")
    parser.add_argument("--trials", type=int, default=3)
    parser.add_argument("--output", type=Path, default=Path("codex-state-report.json"))
    args = parser.parse_args()
    config = json.loads(args.config.read_text())
    validate(config)
    if not 1 <= args.trials <= 10:
        parser.error("trials must be between 1 and 10")
    if not args.run:
        print("Configuration valid; no requests sent. Use --run to execute paired probes.")
        return
    for arm in config["arms"]:
        for field in ("credential_env", "chatgpt_account_id_env", "proxy_env"):
            if field in arm and not os.environ.get(arm[field]):
                parser.error("A required credential/proxy environment variable is missing")
    results = []
    suite = cases()
    report = {"schema": 1, "harness": "responses-http-not-official-codex", "model": config["model"],
              "reasoning_effort": config["reasoning_effort"],
              "suite_sha256": hashlib.sha256(json.dumps(suite, sort_keys=True).encode()).hexdigest(),
              "account_parity": "operator_must_verify_gateway_account_and_exit", "results": results}
    # Exclusive, owner-only file: never overwrite an existing report/symlink.
    fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        try:
            for trial in range(args.trials):
                for index, case in enumerate(suite):
                    order = (0, 1) if (trial + index) % 2 == 0 else (1, 0)
                    for arm_index in order:
                        result = probe(config, config["arms"][arm_index], case)
                        result.update(arm="AB"[arm_index], trial=trial + 1, case=case["id"])
                        results.append(result)
        finally:
            report["summary"] = summarize(results)
            json.dump(report, stream, indent=2)
            stream.write("\n")
    print(json.dumps(report["summary"], indent=2))


if __name__ == "__main__":
    main()
