# OpenAI Non-Streaming Overload Failover

## Goal

Apply the existing OpenAI overload retry policy to non-streaming `/v1/responses` requests when the OAuth upstream returns an SSE body ending in `response.failed` with a capacity-shed signal.

## Design

The non-streaming SSE-to-JSON adapter must inspect the terminal failure before writing any downstream bytes. When the terminal payload is an existing OpenAI capacity-shed signal, it returns the same `UpstreamFailoverError` used by the streaming path. That error already carries the request-scoped transient flag, a 10-retry same-account limit, and a 200 ms retry delay.

The handler therefore reuses its existing retry and account-switch loop. Exhausting the retries must not temporarily unschedule the account. Non-capacity `response.failed` events retain the current protocol-error response behavior.

Passing the selected account into the SSE-to-JSON adapter is required so the existing error constructor can preserve account attribution in operations logging. No database, settings, account, or scheduling schema changes are required.

## Verification

- A non-streaming OAuth SSE overload response returns `UpstreamFailoverError` before writing to the client.
- The error carries retry limit 10, delay 200 ms, and request-scoped transient metadata.
- A non-overload `response.failed` still writes the existing protocol error response.
- Existing OpenAI capacity-shed and handler failover tests remain green.
