# OpenAI OAuth No-op Tool Call 429 Retry Policy

## Goal

Add a child option to the existing OpenAI OAuth no-op tool call injection option. When both options are enabled, an upstream 429 is retried on the same account three times before the current request switches accounts, and the account is not placed into persistent or runtime cooldown.

## Configuration

- Store the child boolean in `accounts.extra.openai_oauth_inject_noop_toolcall_ignore_429_cooldown`.
- The child option is effective only when both it and `accounts.extra.openai_oauth_inject_noop_toolcall` are strict boolean `true`.
- Missing and non-boolean values mean disabled.
- The option applies only to OpenAI OAuth accounts.
- New and existing accounts default to disabled.
- Turning off the parent option clears the child value in create, edit, and bulk-edit forms.

## Request-local Retry Rule

The retry budget belongs to one user request and one selected account. It is never shared across requests or accounts.

For an enabled account:

1. The first upstream 429 retries the same account.
2. The second upstream 429 retries the same account.
3. The third upstream 429 retries the same account.
4. The fourth upstream 429 excludes the account from the current request and switches to another account.

The initial attempt plus three retries produces at most four attempts on one account. A newly selected account receives its own four-attempt budget. A new user request starts with empty retry state.

Same-account retries retain the existing 500 ms retry delay. A successful response ends the retry sequence. A non-429 error follows its existing retry and failover policy rather than being counted as part of the 429 sequence.

## Cooldown And Scheduling

For every OpenAI 429 received by the general account rate-limit handler while the child option is effective:

- Do not write `rate_limit_reset_at`.
- Do not create an in-memory runtime scheduling block.
- Do not pause or disable the account.
- Do not clear a cooldown or scheduling block that existed before the option became effective.

After the fourth 429, the account is excluded only from the current request. A later request may select it immediately if no pre-existing scheduling state blocks it.

Cooldown suppression applies to OpenAI 429 categories that reach the general account rate-limit handler, including 5-hour, 7-day, 30-day, transient, and unidentified rate limits. This remains account-level behavior even on Embeddings and Alpha Search; only their fixed same-account retry behavior is excluded.

Image-generation requests are excluded from the new policy. An image 429 follows the existing image failover and image-capability cooldown flow immediately.

Embeddings and Alpha Search requests are also excluded from the fixed same-account retry policy and retain their existing immediate failover behavior.

## Preserved Side Effects

The feature preserves:

- OpenAI quota-header snapshots and quota observations.
- Observed plan-type synchronization.
- Global OpenAI OAuth 429 storm counting and metrics.
- The normal maximum account-switch limit.
- Existing behavior for accounts where the child option is not effective.

The enabled request bypasses the existing OpenAI OAuth storm early-stop decision so that storm protection cannot terminate the request before the required fourth 429 and account switch. Storm observations are still recorded.

## Backend Design

Use request-local failover metadata instead of a process-wide account counter.

- Add a shared predicate for OpenAI OAuth accounts that requires the parent and child values to be strict boolean `true`.
- When the predicate is true and the upstream status is 429, return a failover error that requests same-account retries with an explicit limit of three retries.
- Extend the existing handler retry path to honor the explicit retry limit independently of pool-mode configuration.
- After the explicit retry limit is exhausted, preserve the existing current-request exclusion and next-account selection flow.
- Short-circuit persistent and runtime cooldown mutation before any cooldown write. Do not write a cooldown and clear it afterward because concurrent scheduling can observe the temporary state.
- Apply fixed same-account retries only to Responses, Chat Completions, passthrough, and applicable WebSocket paths that carry injected user-message input for OpenAI OAuth accounts.
- Keep image, Embeddings, and Alpha Search requests on their existing immediate failover behavior.

## Administration UI

Show a disabled-by-default child toggle under the existing no-op tool call injection toggle in OpenAI OAuth create, edit, and bulk-edit forms.

Suggested Chinese label:

`429 时原账号连续重试 3 次后切换，且不暂停调度`

The child control is disabled or hidden when the parent is off. Saving with the parent off removes the child key from `extra`.

## Delivery Slices

Keep each implementation slice at three changed files or fewer:

1. Backend option predicate, failover metadata, and focused unit tests.
2. Runtime and persistent 429 mutation suppression with focused unit tests.
3. Responses and Chat Completions handler retry integration with tests.
4. Image, alpha-search, media, and WebSocket integration, split further when needed.
5. Chinese and English translations plus one account form.
6. Remaining create, edit, and bulk-edit forms, split further when needed.
7. Frontend behavior tests, split by modal.

## Risks And Tests

- An off-by-one error could switch after three total attempts instead of four. Test the first three 429s as same-account retries and the fourth as a switch.
- Existing pool-mode retry counts could override the fixed feature limit. Test enabled accounts with pool mode off and with custom pool retry counts of zero and ten.
- A cooldown could still be written through one of the two 429 state paths. Test both runtime and repository state after header-based, body-based, and unidentified 429s.
- Image 429s could accidentally inherit the fixed retry metadata or skip their existing image-capability cooldown. Test that they keep an explicit same-account retry limit of zero, write the image-capability cooldown, and immediately enter failover.
- Storm protection could stop the request before switching. Test the enabled behavior while the global storm threshold is active.
- Retrying streamed requests after output begins is unsafe. Preserve the existing committed-output guard and test that no retry occurs after semantic output is written.
- Request-local state could leak between accounts or requests. Test independent counters for accounts A and B and a fresh counter for a new request.
- Parent-child UI state could persist an orphan child value. Test create, edit, and bulk-edit saves with the parent turned off.
