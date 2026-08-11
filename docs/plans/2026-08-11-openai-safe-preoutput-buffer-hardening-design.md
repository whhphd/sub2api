# OpenAI OAuth Safe Pre-Output Buffer Hardening

## Problem

The safe pre-output overload retry switch classifies structural Responses SSE
events as non-semantic, but the standard stream path still stores them in a
4 KiB `bufio.Writer`. A large `response.created` or `response.in_progress`
event therefore bypasses that buffer and commits bytes to the client before a
later `response.failed` overload event arrives. The handler then correctly
observes that the response is already written, but the retry is no longer safe.

## Approved Design

When the global safe pre-output overload retry setting is enabled for an
OpenAI OAuth account, the standard Responses stream path will enable the
existing `openAIFirstOutputStage` for the whole pre-semantic-output phase. The
stage is bounded at 8 MiB, spills to an unlinked temporary file after the
memory threshold, and is discarded on a pre-output failover. It is committed
only from the existing guarded event-boundary path when the first semantic
output event is complete.

The existing first-output timeout behavior remains unchanged when the setting
is disabled. Passthrough streaming keeps its existing pending-line buffer.
After semantic output has been committed, response handling continues to use
the current no-retry-on-partial-stream behavior.

## Verification

- Add a standard Responses stream regression test with >4 KiB `created` and
  `in_progress` events followed by `error` and `response.failed`; assert a
  `UpstreamFailoverError` and zero downstream bytes.
- Retain existing passthrough, metadata, semantic-output, and disabled-setting
  tests.
- Run the focused service tests, the full backend test suite, then production
  health and bounded streaming overload checks after deployment.
