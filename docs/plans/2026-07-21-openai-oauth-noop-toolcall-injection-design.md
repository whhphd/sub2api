# OpenAI OAuth No-op Tool Call Injection

## Goal

Allow an administrator to enable a per-account compatibility behavior for OpenAI OAuth accounts. When enabled, a normal user turn is sent upstream with a paired `custom_tool_call` and `custom_tool_call_output` representing an empty successful `true` command execution.

## Configuration

- Store the boolean in `accounts.extra.openai_oauth_inject_noop_toolcall`.
- Missing or non-boolean values mean disabled.
- The option applies only to accounts where `platform == "openai"` and `type == "oauth"`.
- Existing and newly created accounts default to disabled.

## Injection Rule

Inject only when all conditions hold:

1. The selected upstream account has the account option enabled.
2. The request is not `/responses/compact`.
3. `input` is a non-empty array.
4. The last input item has `type == "message"` and `role == "user"`.

Append exactly two items with a fresh shared call ID:

1. A `custom_tool_call` named `exec`, containing the fixed no-op command payload.
2. A `custom_tool_call_output` containing the fixed empty successful execution output.

The helper must not inject again when the final item is already a tool output. This makes retries of an already transformed body naturally idempotent.

## Request Paths

- HTTP Responses: inject during the existing OpenAI OAuth body transformation, after input normalization and before serialization to the ChatGPT Codex upstream.
- Responses WebSocket v2: apply the same helper to every normalized `response.create` payload before it is written upstream.
- API key, upstream passthrough, Grok, and other platform/account types remain unchanged.

## Administration UI

Expose a disabled-by-default toggle in OpenAI OAuth account create, edit, and bulk-edit forms. Persist it in `extra`; do not add a database column or migration.

## Delivery Slices

1. HTTP account option, injection helper, and unit tests.
2. WebSocket integration and tests.
3. Create/edit/bulk-edit UI, types, translations, and UI tests, split further as needed to keep each task within the repository's three-file change limit.

## Risks And Tests

- Upstream request validation may change: verify the exact call/output wire shape against ChatGPT Codex.
- Incorrect scope could affect API key accounts: test account type and default-off behavior.
- Compact requests may reject tool items: test that compact never injects.
- Retried or replayed payloads could duplicate items: test last-item gating and repeated transformation.
- HTTP and WebSocket behavior could drift: use the same injection helper and assert equivalent payloads.
