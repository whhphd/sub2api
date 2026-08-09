# OpenAI OAuth Global Runtime Policy Design

## Goal

Replace the per-account noop tool-call injection and consecutive-429 controls
with two independent global runtime policies in System Settings:

- Noop Exec tool-call injection for all OpenAI OAuth accounts.
- Dynamic 429 scheduling based on an account-local fixed observation window.

The new policies apply equally to existing and newly added OpenAI OAuth
accounts. Existing account `extra` fields remain stored for compatibility but
are ignored at runtime and are no longer exposed or written by account forms.

## Scope

Tool-call injection continues to apply only to normal OpenAI OAuth Responses
text turns whose final input item is a user message. HTTP Responses and
Responses WebSocket transports are included. Compact requests are excluded.

Dynamic 429 scheduling applies to these text gateway paths:

- `/responses`
- `/v1/chat/completions`
- OpenAI-backed `/v1/messages`
- Responses WebSocket

Images, Embeddings, Alpha Search, `/v1/responses/input_tokens`, compact,
account tests, and quota probes remain on their existing upstream error paths
and do not contribute samples.

## Global Configuration

Store one JSON document under `openai_oauth_runtime_settings`. It contains two
logical settings that can be enabled independently:

```json
{
  "noop_toolcall_injection_enabled": true,
  "dynamic_429_scheduling": {
    "enabled": true,
    "window_seconds": 300,
    "minimum_samples": 20,
    "minimum_429_count": 3,
    "ratio_threshold": 1.0,
    "pause_mode": "upstream_reset",
    "fixed_pause_seconds": 60,
    "revision": 1
  }
}
```

Accepted ranges:

- `window_seconds`: 60-3600
- `minimum_samples`: 2-10000
- `minimum_429_count`: 1 through `minimum_samples`
- `ratio_threshold`: 0.01-1
- `fixed_pause_seconds`: 1-7200
- `pause_mode`: `upstream_reset` or `fixed`

The backend validates every enabled configuration. Disabled configurations are
normalized to valid defaults rather than allowing invalid values to persist.
Ratio comparisons use integer basis points and cross multiplication rather
than floating-point equality at the decision boundary.

### Initial Compatibility

When the new setting is absent, read the existing
`openai_oauth_new_account_noop_toolcall_defaults_enabled` setting. If it is
true, both new global features default to enabled; otherwise both default to
disabled. The dynamic defaults are 300 seconds, 20 samples, three 429s, a
ratio of 1.0, upstream-reset pause mode, and a retained fixed value of 60
seconds.

After the new setting has been saved once, only the new setting controls
runtime behavior. No account data migration is required.

## Runtime Settings Cache

`SettingService` exposes a validated, immutable policy snapshot through a
short-lived in-memory cache. Concurrent cache misses use singleflight. A
successful admin update immediately invalidates the cache. Temporary database
failures use the last valid snapshot when one exists.

If startup has no valid snapshot and the database read fails, injection is
disabled and dynamic scheduling is disabled, so 429 responses use the original
sub2api behavior. Invalid persisted JSON produces an explicit warning and the
same safe fallback.

## Fixed Observation Window

Use Redis because scheduling may run on multiple application instances. Each
OpenAI OAuth account has an independent window containing:

- Policy revision
- Window start time
- Total sample count
- 429 sample count
- Most recent normalized upstream reset time, when available
- Triggered state

The first non-429 outcome does not create a window. The first 429 creates a
window and counts as its first sample. Successes and other upstream errors are
then counted until the anchored window expires.

After every sample, pause only when all three conditions hold:

```text
total_samples >= minimum_samples
count_429 >= minimum_429_count
count_429 / total_samples >= ratio_threshold
```

If the fixed window expires without meeting the conditions, Redis discards it
and waits for the next 429 to start a new window. A policy revision change also
discards the old window so samples collected under different parameters are
never mixed.

A Lua script performs window creation, expiry, increments, threshold checks,
and trigger claiming atomically. Only one application instance can claim the
pause decision. Redis state expires automatically and is not a source of
permanent account status.

## Request And Failover Behavior

When dynamic scheduling is disabled, all 429 responses follow the original
sub2api pause and failover behavior.

When it is enabled for an OpenAI OAuth account:

1. Normalize and record the upstream outcome.
2. Preserve existing plan and Codex rate-limit snapshot side effects.
3. If a 429 does not meet the dynamic threshold, suppress account cooldown for
   that response only.
4. Add the account to the current request's failed-account set and select a
   different account.
5. Never retry the ordinary OAuth 429 on the same account.

The failed account remains excluded for the rest of the current request even
though it is still globally schedulable. The request continues until another
account succeeds, no other account is available, or the existing maximum
account-switch budget is exhausted.

If a success sample is the sample that first satisfies the threshold, the
successful response remains successful. The account is paused as a state side
effect after the observation.

## Pause Modes

### Upstream Reset

Reuse the semantics of the original OpenAI 429 pause chain. Normalize reset
information when a 429 is observed and keep only the most recent reset timestamp
in Redis; do not persist raw response bodies or headers.

When the threshold is reached:

1. Use the most recent still-future `x-codex-*` 5h/7d reset time.
2. Otherwise use a still-future reset time parsed from the 429 response body.
3. Otherwise reuse the existing configurable 429 fallback cooldown.
4. If that fallback is disabled, do not manufacture a new pause duration.

### Fixed

Pause until `now + fixed_pause_seconds`, ignoring upstream reset duration for
the pause decision. Existing plan and quota snapshot persistence still runs.

Both modes write the existing rate-limited account state and runtime scheduling
block. They do not create a parallel status mechanism.

## Failure Handling

- Redis failure while handling a 429 falls back immediately to the original
  429 pause path. This prevents unlimited retries when shared state is down.
- Redis failure while recording success or another error only logs a warning
  and does not alter the current response.
- Database or settings-cache failure follows the safe settings fallback above.
- Concurrent pause attempts are deduplicated by the Redis trigger claim.
- A pause write failure is logged through existing account-state error handling;
  the request continues according to existing failover rules.

## Admin UI And API

Replace the existing new-account defaults row in System Settings with two
sibling global cards under scheduling settings.

The tool-call injection card has a global toggle, scope text, and its own save
action.

The dynamic scheduling card follows the approved reference layout and contains:

- Enable toggle
- Observation window
- Minimum samples
- Minimum 429 count
- 429 ratio threshold
- Pause mode
- Fixed pause seconds
- Save action

When dynamic scheduling is disabled, its parameters remain visible but are
disabled. When upstream-reset mode is selected, the fixed-seconds control is
disabled but its value is retained.

A dedicated admin API reads the complete runtime policy and accepts partial
updates for either logical section. The service merges, validates, increments
the policy revision when dynamic settings change, persists the single JSON
document, invalidates the runtime cache, and records the change in the existing
admin audit trail.

Remove the old controls and payload writes from OpenAI OAuth account creation,
editing, and bulk editing. Existing unrelated account controls remain intact.

## Verification

Backend coverage includes:

- Legacy-setting inheritance and new-setting precedence
- Range and cross-field validation
- Cache hits, invalidation, last-known-good behavior, and corrupt JSON fallback
- Redis first-429 start, non-429 no-op, expiry, revision reset, and atomic claim
- Every threshold condition and ratio boundary
- Redis failure fallback
- Fixed and upstream-reset pause modes
- Success-triggered pause without response failure
- Request-local account exclusion and cross-account failover
- Global injection behavior and ignored legacy account fields
- Explicit exclusions for non-text endpoints

Frontend coverage includes loading and saving both cards, pause-mode control
state, parameter validation, responsive rendering, and removal of the three
account-level editors. Run targeted and full Go tests, targeted and full
frontend tests, TypeScript checks, and the production frontend build.

## Implementation Boundaries

The repository rule limits each implementation batch to at most three changed
files. Work is divided into small commits for settings/cache, admin API/audit,
Redis state, service policy, handler failover, injection, each account editor,
the System Settings UI, and final regression coverage. No production deployment
is part of an implementation batch until the complete test suite and image
build have passed.
