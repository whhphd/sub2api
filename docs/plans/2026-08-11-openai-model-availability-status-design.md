# OpenAI Model Availability Status Design

## Goal

Return `404 model_not_found` when every account candidate in the selected group rejects the requested model through its account model mapping. This applies to any model name and is independent of the custom `/v1/models` display list.

## Classification

The scheduler already records why each candidate was filtered. It will attach a structured marker to its existing `ErrNoAvailableAccounts` error only when:

- the candidate pool is non-empty;
- every candidate is filtered as `model_not_supported`; and
- no temporary exclusion reason is present.

Handlers use that marker to return `404 model_not_found`. Empty pools, rate limits, quota pauses, runtime blocks, explicit request exclusions, and mixed permanent/temporary reasons retain the existing capacity response, normally `503`.

## Scope

Apply the classification consistently to OpenAI Responses, Chat Completions, and Messages-compatible entry points. Preserve each request's resolved platform so OpenAI-compatible routing behavior is unchanged.

The custom `/v1/models` list remains display-only and does not participate in scheduling or status classification.

## Compatibility

The marked scheduler error continues to unwrap to `ErrNoAvailableAccounts`, preserving existing `errors.Is` checks and fallback behavior. Handler code reads the structured marker rather than parsing diagnostic strings.

## Tests

Cover four scheduler outcomes:

- all candidates report `model_not_supported`: marked as permanent model unavailability;
- quota-paused candidate: not marked;
- mixed model and temporary exclusion reasons: not marked;
- empty candidate pool: not marked.

Run the complete service and handler package test suites after the focused regression tests.
