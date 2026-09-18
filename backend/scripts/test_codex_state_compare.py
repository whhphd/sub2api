import importlib.util
import io
import json
import os
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("probe", Path(__file__).with_name("codex_state_compare.py"))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class Response(io.BytesIO):
    status = 200

    def __init__(self, body, status=200, headers=None):
        super().__init__(body)
        self.status = status
        self.headers = headers or {"Content-Type": "text/event-stream", "x-codex-turn-state": "private-state"}


class CompareTests(unittest.TestCase):
    def test_status_and_sensitive_output(self):
        raw = b'data: {"type":"response.failed","response":{"error":{"code":"usage_limit_reached","message":"private credential"}}}\n\n'
        output, state, meta = probe.consume(Response(raw, status=292), probe.time.monotonic())
        self.assertEqual(meta["http_status"], 292)
        self.assertEqual(meta["error_class"], "quota_exhausted")
        self.assertEqual(state, "private-state")
        self.assertEqual(output, [])
        self.assertNotIn("private", json.dumps(meta))

    def test_missing_terminal_and_multiline(self):
        _, _, meta = probe.consume(Response(b'data: {"type":"response.created"}\n\n'), probe.time.monotonic())
        self.assertEqual(meta["terminal"], "missing")
        raw = b'data: {"type":"response.completed",\ndata: "response":{"output":[]}}\r\n\r\n'
        _, _, meta = probe.consume(Response(raw), probe.time.monotonic())
        self.assertEqual(meta["terminal"], "response.completed")
        with self.assertRaises(ValueError):
            probe.consume(Response(b"data: " + b"x" * probe.MAX_LINE), probe.time.monotonic())

    def test_json_error(self):
        _, _, meta = probe.consume(Response(b'{"error":{"code":"overloaded","message":"secret"}}', 503, {"Content-Type": "application/json"}), probe.time.monotonic())
        self.assertEqual(meta["error_class"], "overload")
        self.assertNotIn("secret", json.dumps(meta))

    def test_tool_state_only_within_turn_and_grading(self):
        seen = []

        def fake_request(arm, payload, session, state=""):
            seen.append((session, state))
            meta = {"terminal": "response.completed"}
            if not state:
                return [{"type": "function_call", "name": "lookup_marker", "call_id": "c1", "arguments": '{"key":"R728"}'}], "first-state", meta
            return [{"type": "message", "content": [{"type": "output_text", "text": '{"answer":"violet-cedar"}'}]}], "second-state", meta

        config = {"model": "gpt-test", "reasoning_effort": "high"}
        with patch.object(probe, "request", side_effect=fake_request):
            for _ in range(2):
                result = probe.probe(config, {}, probe.cases()[-1])
                self.assertTrue(result["passed"])
        self.assertEqual([x[1] for x in seen], ["", "first-state", "", "first-state"])
        self.assertEqual(seen[0][0], seen[1][0])
        self.assertNotEqual(seen[0][0], seen[2][0])

    def test_tool_must_really_run(self):
        result = ([{"type": "message", "content": [{"type": "output_text", "text": '{"answer":"violet-cedar"}'}]}], "", {"terminal": "response.completed"})
        with patch.object(probe, "request", return_value=result):
            row = probe.probe({"model": "gpt-test", "reasoning_effort": "high"}, {}, probe.cases()[-1])
        self.assertFalse(row["passed"])

    def test_credentials_and_redirect_do_not_leak(self):
        paths = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                paths.append(self.path)
                self.send_response(307)
                self.send_header("Location", "/stolen")
                self.end_headers()

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            arm = {"base_url": "http://127.0.0.1:" + str(server.server_port), "credential_env": "PROBE_TEST_SECRET"}
            with patch.dict(os.environ, {"PROBE_TEST_SECRET": "secret", "HTTPS_PROXY": "http://bad.invalid"}):
                _, _, meta = probe.request(arm, {"stream": True}, "test")
            self.assertEqual(paths, ["/responses"])
            self.assertEqual(meta["http_status"], 307)
            self.assertNotIn("secret", json.dumps(meta))
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_validate(self):
        arm = {"base_url": "https://example.com/v1", "credential_env": "TEST_KEY"}
        config = {"model": "gpt-test", "reasoning_effort": "xhigh", "arms": [arm, arm]}
        probe.validate(config)
        for url in ("http://example.com", "https://u:p@example.com", "https://example.com?key=secret"):
            with self.assertRaises(ValueError):
                probe.validate(dict(config, arms=[dict(arm, base_url=url), arm]))

    def test_malformed_upstream_is_bounded_failure(self):
        for value in ([], {"type": "response.completed", "response": "invalid"}, {"type": "response.completed", "response": {"output": [None]}}):
            with self.assertRaises(ValueError):
                probe.consume(Response(b"data: " + json.dumps(value).encode() + b"\n\n"), probe.time.monotonic())
        self.assertFalse(probe.grade(probe.cases()[0], [{"type": "message", "content": [None]}]))


if __name__ == "__main__":
    unittest.main()
