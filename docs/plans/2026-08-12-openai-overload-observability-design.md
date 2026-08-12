# OpenAI OAuth overload observability design

## Goal

When the global OpenAI OAuth safe pre-output overload retry setting is enabled,
emit structured logs that distinguish upstream overload detection, retry,
recovery, and user exposure. Keep the existing logging behavior unchanged when
the setting is disabled.

## Scope and event semantics

The instrumentation covers OpenAI OAuth streaming Responses, Chat Completions,
and Messages requests. `overload_detected` is emitted once per upstream attempt
that is classified as an overload. `overload_retried` is emitted when that
attempt is actually retried, with `retry_scope` identifying same-account or
next-account retry. `overload_recovered` is emitted once when a later attempt
succeeds. `overload_exposed` is emitted once when the request finally returns
the overload after no safe retry remains.

Every event carries request/account/model/endpoint context and stream-stage
diagnostics: the last SSE event type, event count, whether semantic output had
started, semantic output kind, and whether downstream output was committed.
Structural events alone do not count as semantic output; text/tool/reasoning/
image deltas do.

## Implementation boundary

The handler owns the request lifecycle and is the only instrumentation layer.
The existing service-level overload classification remains the source of truth.
No database schema, settings API, or UI changes are needed. The runtime setting
is read only for OpenAI OAuth accounts, and disabled mode executes no new
classification or logging path.

## Verification

Add unit coverage for stage classification and for detected/retried/recovered/
exposed event gating. Run focused service/handler tests and inspect production
logs after deployment.
