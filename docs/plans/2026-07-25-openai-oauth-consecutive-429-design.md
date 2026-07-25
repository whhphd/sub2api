# OpenAI OAuth Consecutive 429 Scheduling Design

## Goal

Replace the experimental request-local three-retry behavior with an
account-level consecutive 429 threshold. For an eligible OpenAI OAuth account,
the first nine consecutive 429 responses do not pause scheduling. The tenth
consecutive 429 falls through to the original sub2api rate-limit handling so
the account is paused until the upstream reset time and resumes automatically.

## Eligibility And Scope

The policy is enabled only when both account extras are strict boolean `true`:

- `openai_oauth_inject_noop_toolcall`
- `openai_oauth_inject_noop_toolcall_ignore_429_cooldown`

It applies to the text gateway paths that support the injected no-op tool call:
Responses HTTP, Chat Completions, Messages compatibility, and Responses
WebSocket. Images, Embeddings, and Alpha Search retain their original 429
behavior and do not participate in the consecutive counter.

## Redis Counter

Use an atomic Redis counter keyed by account ID. Increment and TTL initialization
are performed in one Lua script so concurrent requests cannot lose updates.
The key has a 35-day safety TTL to prevent abandoned keys from persisting
forever. Any non-429 upstream response deletes the account counter, including
successful responses and other HTTP errors.

If Redis incrementing fails, fail safe by using the original sub2api 429 path
for that response. A Redis outage must not make an exhausted account remain
schedulable indefinitely.

## 429 Flow

For an eligible request path and account:

1. Preserve the existing plan-type synchronization, Codex quota snapshots,
   storm metrics, and operational error records.
2. Increment the account's consecutive 429 counter.
3. Counts 1 through 9 suppress both the persistent rate-limit write and the
   runtime scheduling block.
4. The request immediately excludes the account and attempts another account;
   it does not retry the same account solely because of this feature.
5. At count 10, stop suppressing state changes and call the original runtime
   and persistent 429 logic unchanged.
6. The original logic parses `x-codex-*` headers or
   `usage_limit_reached.resets_at`, writes `rate_limit_reset_at`, applies its
   existing fallback cooldown when needed, and restores scheduling after the
   reset time.

The Redis key is deleted when the threshold is reached because the durable and
runtime scheduling state becomes authoritative.

## Compatibility

Keep the existing account extra key to avoid a database migration and preserve
the current UI state of existing accounts. Update only the label and help text
to describe the new threshold behavior.

The generic pool-mode same-account retry behavior remains unchanged. Only the
feature-specific forced three retries and explicit retry-limit override are
removed.

## Tests

- Redis increments atomically, initializes a 35-day TTL, and resets by account.
- Counts 1 through 9 suppress original scheduling state mutations.
- Count 10 invokes the existing reset-time scheduling path.
- Redis errors immediately use the original 429 path.
- A non-429 error and a successful usage record both reset the counter.
- The first 429 switches accounts immediately without a feature-specific retry.
- Images, Embeddings, and Alpha Search retain original behavior.
- HTTP and WebSocket text paths share the same account-level threshold.
