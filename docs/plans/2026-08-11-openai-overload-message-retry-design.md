# OpenAI overload message retry design

## Problem

OpenAI OAuth Responses streams can terminate with `response.failed` whose message is
`Our servers are currently overloaded. Please try again later.` but whose error code
is absent. The existing capacity-shed classifier recognizes only
`server_is_overloaded` and `slow_down`, so the message-only form does not receive the
same bounded same-account retry behavior.

## Design

- Extend the existing capacity-shed classifier to accept the extracted error message.
- Match the known OpenAI overload sentence case-insensitively after trimming space.
- Reuse the existing `UpstreamFailoverError` path. The error remains request-scoped,
  retries on the same account using the existing retry count and delay, then falls
  back to the existing account-switch behavior.
- Do not mark the account temporarily unschedulable for this request-scoped signal.
- Retry only before semantic output has been written. Streams that already emitted
  text or tool calls keep the current behavior to avoid duplicate output or actions.
- Do not change 429 dynamic scheduling, account settings, database schema, images,
  embeddings, Alpha Search, or other unrelated endpoints.

## Tests

- Verify the exact message-only overload event is classified as capacity shed.
- Verify it produces a retryable, request-scoped failover error for an OpenAI OAuth
  account without requiring pool-mode configuration.
- Verify unrelated transient messages are not reclassified as capacity shed.

