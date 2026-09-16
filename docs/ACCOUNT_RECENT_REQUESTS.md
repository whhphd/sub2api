# Account recent request timeline

The administrator account list shows up to ten records per account from the last
24 hours. Newest records appear on the right. Hover opens details; click pins
them while the list refreshes. A failed refresh keeps the previous records and
shows a stale-data label. This is a log view, not live concurrency or a user
success-rate calculation: error records can coexist with a successful retry.

## Source and adaptations

The timeline, details component, cache-ratio utility, and component regression
tests are ported from [DeanZFC/sub2api-custom](https://github.com/DeanZFC/sub2api-custom)
at `2dbdb846b8fe9db6aa73b8bfd6d8b608c5bb36a0`, under the existing LGPL-3.0 license.
Source attribution is retained in the copied files. CallAI adaptations:

- `GET /api/v1/admin/ops/accounts/recent-requests?account_ids=1,2` reads local
  usage/error logs through the existing administrator authorization and Ops
  monitoring gate. It accepts at most 100 positive IDs and deduplicates them.
- One SQL query uses bounded lateral account/time lookups, merges the latest
  successes and errors, and joins identity metadata only for the retained rows.
  Existing account/time indexes are reused; no migration is required.
- Responses contain only timeline/detail fields, never request bodies, headers,
  or credentials. Error text passes through existing secret redaction helpers.
- Request types use the local numeric enums. Account cost uses the existing
  `COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)`
  formula. Missing values remain null and real zero remains zero.
- Refresh runs independently every five seconds, in batches of at most 100 IDs
  and with one in-flight batch. Larger pages are chunked. Hiding the column/page,
  loading the list, opening a management modal, or unmounting cancels pending
  work. Resuming refreshes immediately; stale page responses are discarded.
- Record identity uses log kind and row ID, so duplicate/missing request IDs do
  not merge different records. Pinned details stay available as records age out.

## Validation (2026-09-16)

- Backend `go test ./...` on the server.
- PostgreSQL integration test: per-account limits, mixed success/error order,
  24-hour boundaries, unrelated-account isolation, numeric request types,
  missing usage, zero values, and account-cost snapshot calculation.
- Service/handler checks: bounded query time, positive IDs, deduplication,
  monitoring gate, empty account results, and error-text redaction.
- Frontend: 289 files / 2202 tests, TypeScript, Vite build and changed-file ESLint.
  Includes donor interaction tests and refresh/cancellation/partial-failure tests.
- Backend golangci-lint: zero issues.
- Desktop/mobile/dark-mode visual checks skipped at the user's request.

Only the recent request feature is included. TPS, group-layout changes and
upstream-following subscription resets are not part of this change.
