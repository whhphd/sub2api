# OpenAI OAuth Global Runtime Policy Implementation Plan

Design: `docs/plans/2026-08-09-openai-oauth-global-runtime-policy-design.md`

Each batch changes no more than three files and ends with focused tests and a
small commit. Existing untracked branding assets are excluded from every
commit.

## Batch 1: Settings Model

Files:

- `backend/internal/service/domain_constants.go`
- `backend/internal/service/settings_view.go`
- `backend/internal/service/openai_oauth_runtime_settings_test.go`

Add the persisted key, typed global policy, defaults, clone helpers, pause-mode
constants, normalization, and strict validation. Test defaults and all boundary
conditions.

## Batch 2: Cached Settings Service

Files:

- `backend/internal/service/setting_service.go`
- `backend/internal/service/setting_features.go`
- `backend/internal/service/openai_oauth_runtime_settings_test.go`

Add atomic last-known-good caching, singleflight refresh, legacy-key fallback,
partial updates, dynamic revision increments, safe failure behavior, and cache
invalidation. Test inheritance, precedence, corrupt JSON, stale fallback, and
partial updates.

## Batch 3: Admin Runtime API

Files:

- `backend/internal/handler/admin/setting_handler_runtime.go`
- `backend/internal/server/routes/admin.go`
- `backend/internal/handler/admin/setting_handler_openai_oauth_runtime_test.go`

Add GET and PATCH endpoints for the dedicated policy. Accept pointer-based
partial payloads, return the normalized complete policy, and rely on the
existing admin audit middleware plus an explicit changed-setting log.

## Batch 4: Redis Fixed Window

Files:

- `backend/internal/service/openai_oauth_429_counter.go`
- `backend/internal/repository/openai_oauth_429_counter_cache.go`
- `backend/internal/repository/openai_oauth_429_counter_cache_test.go`

Replace streak operations with an atomic fixed-window observation contract.
Implement first-429 activation, anchored expiry, policy-revision reset, integer
ratio comparison, normalized reset hints, and a single trigger claim in Lua.

## Batch 5: Dynamic Policy Core

Files:

- `backend/internal/service/openai_oauth_429_policy.go`
- `backend/internal/service/openai_oauth_429_policy_test.go`
- `backend/internal/service/openai_account_runtime_block_fastpath.go`

Resolve the global policy, submit upstream outcomes, suppress cooldown only
below the threshold, apply fixed or upstream-reset pause decisions, and retain
the original Redis-failure fallback. Keep existing 429 plan/snapshot side
effects.

## Batch 6: Success Observations And Pause State

Files:

- `backend/internal/service/openai_gateway_usage.go`
- `backend/internal/service/ratelimit_service.go`
- `backend/internal/service/ratelimit_service_openai_test.go`

Record successful eligible outcomes, reuse the existing rate-limited database
state and fallback cooldown, and ensure a success-triggered pause cannot turn a
successful response into an error.

## Batch 7: Request-Local Failover

Files:

- `backend/internal/handler/openai_gateway_handler.go`
- `backend/internal/handler/openai_chat_completions.go`
- `backend/internal/handler/openai_gateway_handler_test.go`

Replace per-account flag checks with the global runtime policy on Responses,
Messages, Chat Completions, and WebSocket failover. Prove that a below-threshold
429 excludes the current account, selects another account, and never retries
the same ordinary OAuth account.

## Batch 8: Global Injection Transform

Files:

- `backend/internal/service/openai_codex_transform.go`
- `backend/internal/service/openai_codex_transform_test.go`

Make injection accept an explicit global-policy decision while still requiring
an OpenAI OAuth account, a non-compact request, and a final user message.

## Batch 9: Injection Call Sites

Files:

- `backend/internal/service/openai_gateway_forward.go`
- `backend/internal/service/openai_ws_forwarder_ingress.go`
- `backend/internal/service/openai_ws_forwarder_ingress_test.go`

Load the cached global injection decision in HTTP and WebSocket paths. Verify
enabled, disabled, compact, and non-OAuth behavior.

## Batch 10: Stop Legacy Default Writes

Files:

- `backend/internal/service/openai_oauth_new_account_defaults.go`
- `backend/internal/service/openai_oauth_new_account_defaults_test.go`
- `backend/internal/service/crs_sync_long_context_billing_test.go`

Turn the legacy creation hook into a compatibility no-op so all import and
creation channels stop writing account-level policy fields without requiring
wide call-site churn. Update tests to assert no writes.

## Batch 11: Frontend API And Copy

Files:

- `frontend/src/api/admin/settings.ts`
- `frontend/src/i18n/locales/zh/admin/settings.ts`
- `frontend/src/i18n/locales/en/admin/settings.ts`

Add typed GET/PATCH methods, setting labels, validation messages, pause-mode
copy, and safe loading/saving errors.

## Batch 12: System Settings UI

Files:

- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/views/admin/__tests__/SettingsView.spec.ts`

Remove the old new-account-default row and add the two approved sibling cards.
Implement independent saves, responsive input layout, parameter validation,
and disabled-state behavior.

## Batch 13: Create Account Cleanup

Files:

- `frontend/src/components/account/CreateAccountModal.vue`
- `frontend/src/components/account/__tests__/CreateAccountModal.spec.ts`

Remove the account-level controls, local state, payload writes, and obsolete
tests while preserving all unrelated OpenAI OAuth options.

## Batch 14: Edit Account Cleanup

Files:

- `frontend/src/components/account/EditAccountModal.vue`
- `frontend/src/components/account/__tests__/EditAccountModal.spec.ts`

Remove account-level policy editing and ensure updates leave existing legacy
extra keys untouched instead of rewriting them.

## Batch 15: Bulk Edit Cleanup

Files:

- `frontend/src/components/account/BulkEditAccountModal.vue`
- `frontend/src/components/account/__tests__/BulkEditAccountModal.spec.ts`

Remove bulk policy controls and payload writes without changing other bulk
operations.

## Batch 16: Regression And Build

No source changes unless a failing test identifies an in-scope defect. Run:

- Focused Go tests for settings, Redis, policy, rate limits, transforms, and handlers
- `go test ./...`
- Focused frontend component tests
- Full frontend tests and TypeScript checks
- Production frontend build
- `git diff --check` and a final audit of endpoint exclusions and legacy fields

Deployment is a separate explicit step after the implementation and complete
verification are reported.
