# OpenAI Overload Retry Tuning

## Goal

For OpenAI OAuth capacity-shed/overload stream failures, retry the same account 10 times after the initial attempt, with a 200 ms delay between attempts. After those retries are exhausted, preserve the existing account failover and request-scoped transient behavior.

## Scope

- Apply only to the OpenAI overload signal already recognized by the passthrough service.
- Keep ordinary pool-mode retryable errors at their existing account-configured limit and 500 ms delay.
- Do not change 429 scheduling, account credentials, database schema, or account `pool_mode` settings.

## Design

`UpstreamFailoverError` carries explicit same-account retry metadata. The overload constructor sets a retry limit of 10 and a retry delay of 200 ms. Existing handlers continue to use account configuration as the fallback for other errors, while preferring the explicit metadata for overload errors. This keeps the behavior explicit instead of treating non-pool OAuth accounts as pool-mode accounts merely to obtain the default retry count.

The retry count is per selected account and per request. Ten retries means eleven total attempts on that account including the initial attempt. Once exhausted, the existing failover loop may switch accounts; the overload remains request-scoped transient and must not trigger persistent temporary account unscheduling.

## Verification

- Unit tests assert overload errors expose limit 10 and delay 200 ms.
- Unit tests assert ordinary retryable errors retain existing fallback metadata.
- Handler tests assert the overload path performs ten same-account retries before switching.
- Existing targeted OpenAI service and handler tests remain green.
