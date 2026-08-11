# OpenAI OAuth Safe Pre-Output Overload Retry

## Goal

Allow OpenAI OAuth Responses streams to retry an upstream overload before any
semantic output is visible to the client. Structural SSE events must not commit
an otherwise retryable response, because a later retry would otherwise produce
two response envelopes in one client stream.

## Scope

- Add a global, runtime-toggleable System Settings switch.
- Default the switch to disabled for backward-compatible behavior.
- Apply it only to OpenAI OAuth accounts and Responses streaming paths.
- Reuse the existing pre-output buffering and failover/retry machinery.
- Keep the existing safety boundary: after real semantic output is written,
  do not retry or switch accounts for this optimization.

## Design

When enabled, `response.output_item.added` and `response.content_part.added`
are treated as structural metadata. `response.output_item.done` is treated as
metadata only when its item has no completed content, arguments, result, or
other semantic body. These events stay in the existing pending buffer. If a
retryable overload reaches `response.failed` first, the handler returns the
existing `UpstreamFailoverError` before flushing that buffer. A successful
retry therefore starts with one coherent response stream. The same event
classification is used by passthrough and standard Responses streaming paths.

The setting is stored in the existing JSON runtime policy record, so changing
it takes effect through the existing cache/update path without a database
migration or process restart. The admin endpoint updates it independently from
tool-call injection and dynamic 429 scheduling.

## Verification

- Service tests cover defaults, partial persistence, and the new setting.
- Handler tests cover the dedicated PATCH field and preservation of unrelated
  settings.
- Stream tests cover metadata-only overload failover in passthrough and
  standard paths, semantic `output_item.done`, and the disabled behavior.
- Frontend tests cover loading and independently saving the System Settings
  toggle.
