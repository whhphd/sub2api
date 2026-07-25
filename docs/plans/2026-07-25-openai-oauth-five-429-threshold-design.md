# OpenAI OAuth Five-429 Threshold Design

## Goal

Lower the existing account-level consecutive 429 threshold from 10 to 5 for
eligible OpenAI OAuth text requests. The first four consecutive 429 responses
keep the account schedulable and switch the current request to another account.
The fifth response reuses the original sub2api rate-limit handling, pauses the
account until the upstream reset time, and restores it automatically afterward.

## Scope

Only accounts with both no-op tool-call injection and its 429 scheduling option
enabled use this threshold. Responses HTTP, Chat Completions, Messages
compatibility, and Responses WebSocket remain eligible. Images, Embeddings, and
Alpha Search retain their original behavior.

## Redis State

Keep the existing per-account Redis keys and 35-day safety TTL. Deployment does
not clear existing streaks. An account already at count 5 or higher reaches the
new threshold on its next 429; deployment alone does not pause it. The key is
still deleted when the threshold is reached or when a non-429 response resets
the streak.

## Implementation And Tests

Change the shared threshold constant from 10 to 5, update the unit-test boundary
to cover counts 1, 4, and 5, and update the English and Chinese admin copy. No
database migration, Redis configuration change, or new scheduling path is
required.
