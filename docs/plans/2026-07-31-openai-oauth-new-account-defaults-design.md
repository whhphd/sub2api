# OpenAI OAuth New Account Experimental Defaults

## Goal

Add a global administrator setting that forces every newly created OpenAI OAuth account to start with both the no-op tool-call injection policy and its consecutive-429 scheduling policy enabled.

## Semantics

- The global setting defaults to disabled.
- When enabled, account creation forcibly sets:
  - `openai_oauth_inject_noop_toolcall = true`
  - `openai_oauth_inject_noop_toolcall_ignore_429_cooldown = true`
- The policy applies only when `platform == openai` and `type == oauth`.
- Explicit `false` values supplied by an account creation request are overridden.
- The policy does not add `openai_oauth_inject_noop_toolcall_429_threshold`. If a request supplies a threshold, it is preserved; otherwise the runtime default remains in effect.
- Existing accounts and update paths are unchanged.
- Administrators may disable either account-level setting after creation.

## Coverage

Apply the policy at service-layer account creation boundaries so it covers:

- interactive OpenAI OAuth creation;
- admin-key and Codex session imports;
- account JSON imports;
- batch account creation;
- account duplication;
- CRS-created OpenAI OAuth accounts;
- Spark credential-shadow creation.

## Configuration

Persist the boolean setting in the existing key-value settings table as:

`openai_oauth_new_account_noop_toolcall_defaults_enabled`

Expose it through the existing administrator settings GET/PUT contract and add a toggle near the top of the OpenAI experimental scheduler section. No database schema migration is required.

If the setting cannot be read, account creation returns an error instead of silently creating an account with the wrong policy.

## Implementation

Use one pure helper to clone or initialize account `extra`, check the platform/type, and force the two boolean values. The regular admin account service, duplicate path, CRS sync, and shadow creation call the same helper after reading the global setting.

Implementation work is split into increments of no more than three files:

1. Define and parse the setting.
2. Persist and expose the setting through the administrator API.
3. Apply the creation policy to regular and special creation paths.
4. Add the frontend toggle and translations.
5. Add focused backend and frontend tests.

## Verification

Cover these cases:

- global setting disabled preserves incoming account values;
- global setting enabled overrides explicit `false` values for new OpenAI OAuth accounts;
- API-key and non-OpenAI accounts are unchanged;
- an omitted threshold remains omitted and an explicit threshold is preserved;
- existing-account updates are unchanged;
- all supported creation paths apply the same policy;
- settings API read/write and frontend form submission round-trip the new boolean.
