import json
import unittest
from codex_state_lengths import aggregate


def line(kind, **kw):
    return '2026-09-18T13:07:27+0800\tINFO\tcodex_state_diagnostic\t' + json.dumps(dict(component="service.codex_state_diagnostics", event=kind, **kw))


class LengthTests(unittest.TestCase):
    def test_lengths_separate_from_status_and_no_guard_double_count(self):
        rows = [line("http_send", attempt=1), line("http_state_guard", before_length=312, after_length=312),
                line("http_headers", attempt=1, upstream_status=200, sent_state_length=312, received_state_length=292, model="gpt-test", account_ref="a"*24),
                line("http_body_end", attempt=1, outcome="eof", error_class="overload"),
                line("http_staged_commit", after_length=292)]
        out = aggregate(rows)
        self.assertEqual(out["upstream_http_status"], {"200": 1})
        self.assertEqual(out["state_length_292"], {"sent": 0, "received": 1})
        self.assertEqual(out["state_length_312"], {"sent": 1, "received": 0})
        self.assertEqual(out["by_account_model_length_status"][0]["error_class"], "overload")

    def test_missing_length_is_not_empty(self):
        out = aggregate([line("http_headers", attempt=1, upstream_status=292), line("http_headers", attempt=2, upstream_status=200, sent_state_length=0, received_state_length=0)])
        self.assertEqual(out["received_state_lengths"], {"unobserved": 1, "0": 1})
        self.assertEqual(out["state_length_292"]["received"], 0)
        self.assertEqual(out["headers_missing_send"], 2)

    def test_duplicate_ws_directions_redaction_and_drops(self):
        header = line("http_headers", attempt=1, upstream_status=429, model="Bearer secret", account_ref="secret", received_state_length=312, value="secret")
        rows = [header, header, line("http_body_end", attempt=1, error_class="secret", outcome="secret", suppressed_events=2, observation_truncated=True),
                line("ws_frame", frame_event="response.create", outbound=True, boundary="upstream_send_attempt", sent_state_length=292),
                line("ws_frame", frame_event="response.metadata", outbound=False, boundary="upstream_receive", received_state_length=312)]
        out = aggregate(rows)
        self.assertEqual(out["duplicate_header_records"], 1)
        self.assertEqual(out["http_header_observations"], 1)
        self.assertEqual(out["suppressed_events"], 2)
        self.assertEqual(len(out["ws_state_lengths"]), 2)
        self.assertNotIn("secret", json.dumps(out))


if __name__ == "__main__":
    unittest.main()
