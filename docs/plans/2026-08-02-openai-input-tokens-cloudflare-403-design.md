# OpenAI input_tokens and Cloudflare 403 protection

## Context

OpenAI OAuth requests to `/v1/responses/input_tokens` can receive an HTML 403 from the ChatGPT edge. The generic Responses failover path currently treats that response as an account-level permission failure. A single token-count request can therefore rotate through multiple accounts and place every selected account into a ten-minute temporary-unschedulable state.

Upstream PR #4935 fixes this in two parts. It gives Responses input-token counting dedicated endpoint semantics and separately identifies Cloudflare-generated OpenAI 403 responses before they reach account state handling.

## Scope

Adapt both commits from upstream PR #4935 onto the current fork based on v0.1.169.

1. Recognize `/v1/responses/input_tokens`, `/responses/input_tokens`, and `/backend-api/codex/responses/input_tokens` as token-count endpoints.
2. Select at most one account for a token-count request.
3. For OpenAI OAuth, fall back to the existing local tiktoken estimator when the upstream returns 401, 403, or 404.
4. Do not run generic failover, 403 counters, temporary-unschedulable rules, or permanent account error handling for token-count fallback responses.
5. Continue forwarding OpenAI API-key token-count requests to the official upstream endpoint.
6. Detect narrowly defined Cloudflare-generated HTML 403 responses from OpenAI and return a standard JSON 404 without changing account state.
7. Preserve genuine OpenAI JSON 403 handling.

## Compatibility

The adaptation must retain the current v0.1.169 `guardResponsesSubpath` behavior and all existing fork changes, including OpenAI OAuth no-op tool-call injection, configurable 429 handling, and new-account experiment defaults. Image generation, Embeddings, and Alpha Search behavior remain unchanged.

The proposed general OpenAI OAuth 403 retry switch is deferred. Production should first be observed after the endpoint-specific and Cloudflare-specific causes are removed.

## Integration Strategy

Apply PR #4935 in its original two-commit order and resolve conflicts against the current branch rather than accepting either side wholesale.

1. Integrate dedicated input-token endpoint routing and local fallback.
2. Run endpoint, routing, and count-token tests.
3. Integrate Cloudflare 403 classification and account-state bypass.
4. Run Cloudflare classification, OpenAI gateway, rate-limit, and fork-specific 429/tool-call tests.
5. Run the complete backend test suite and static checks available in the local environment.

No production deployment is part of this change unless separately approved.

## Failure Handling

Invalid input-token request bodies continue to return normal request-validation errors. Local estimation must return at least one token when exact estimation is unavailable, matching the existing count-token fallback contract. Unexpected non-401/403/404 upstream failures remain visible to the client but must not fan out across the account pool.

Cloudflare classification must require multiple edge-response signals and must not convert genuine OpenAI JSON permission errors into 404 responses.

## Test Coverage

- Endpoint normalization for all three input-token paths.
- Dedicated route selection and no ordinary Responses failover.
- OAuth 401, HTML 403, and 404 local fallback.
- API-key upstream forwarding.
- No 403 counter or account-state mutation for token-count fallback.
- Cloudflare HTML/header recognition and standard JSON 404 conversion.
- Genuine JSON 403 preservation.
- Existing no-op tool-call injection and configurable 429 policy regressions.
