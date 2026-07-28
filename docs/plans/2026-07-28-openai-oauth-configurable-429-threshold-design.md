# OpenAI OAuth Configurable 429 Threshold Design

## Goal

Replace the fixed consecutive-429 threshold with an account-level setting for
eligible OpenAI OAuth text requests. Accounts without a stored value use 10.
The configured threshold controls when the existing sub2api rate-limit path
pauses the account until the upstream reset time.

## Storage And Validation

Store the integer in the account `extra` map under
`openai_oauth_inject_noop_toolcall_429_threshold`. This follows the existing
experimental account settings and requires no database migration.

Valid values are integers from 1 through 100. Missing, non-numeric,
non-integral, and out-of-range values fall back to 10 in the backend. Frontend
inputs enforce the same range but are not the security boundary.

The setting is effective only when both existing account options are enabled:

- `openai_oauth_inject_noop_toolcall`
- `openai_oauth_inject_noop_toolcall_ignore_429_cooldown`

Disabling tool-call injection removes both the dependent scheduling option and
the threshold from the submitted account extras.

## Scheduling Behavior

For an effective threshold `N`, consecutive counts below `N` keep the account
schedulable while the current request fails over to another account. Count
`N` falls through to the original sub2api cooldown logic, which uses the
upstream reset time and restores scheduling when it expires.

Changing the configured threshold does not clear the Redis streak. The next
429 compares the incremented count with the new value. Existing non-429 reset
behavior, the 35-day Redis safety TTL, and fail-safe fallback on Redis errors
remain unchanged.

Images, Embeddings, and Alpha Search retain their original behavior and do not
use this threshold.

## Admin UI

Create and edit forms show a numeric threshold input when the two dependent
OpenAI OAuth options are active. A missing stored value initializes the control
to 10.

Bulk edit provides an explicit keep-existing or overwrite choice. Overwrite
accepts one integer from 1 through 100 and updates only selected eligible
accounts. Leaving the control unchanged does not add or remove the threshold
key.

English and Chinese labels describe a configurable threshold instead of a
fixed fifth response.

## Tests

- Backend getter covers the default, numeric representations, fractional
  values, and both range boundaries.
- The scheduling policy uses account thresholds at 1, 10, and 100 and logs the
  effective value.
- Create and edit forms default to 10, validate 1 through 100, persist valid
  values, and remove the key with the parent option.
- Bulk edit preserves existing values by default and overwrites them only when
  explicitly requested.
- Existing image, Embeddings, Alpha Search, failover, Redis reset, and cooldown
  behavior remains covered by the current tests.
