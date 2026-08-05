# ADR 0001: Bounded Model Failure Recovery

- Status: Accepted
- Date: 2026-08-03

## Context

Transient model-provider failures are common, but invisible or unbounded retries make latency, cost, cancellation, and diagnosis unpredictable. Automatically retrying tools is more dangerous because a tool may already have changed external state before returning an error.

The runtime also needs provider fallback without depending on an Enterprise router, database, worker, or provider-specific client type.

## Decision

1. The default model failure policy is fail-fast.
2. Recovery applies only to Model calls. Tools are never retried automatically.
3. `ModelFailurePolicy` returns one typed action: `fail`, `retry`, or `fallback`.
4. The runtime, not the policy, enforces `MaxModelCalls`, Context cancellation, event ordering, and target availability.
5. `TransientModelFailurePolicy` retries ordinary failures only when `ProviderError.Retryable=true`; its separate opt-in context-length action is defined by ADR 0015.
6. A retry uses the same projected State and Model Target with a new Call ID.
7. A fallback moves forward through an ordered, explicitly configured list of Model Targets. It never discovers or selects Enterprise resources.
8. Every physical attempt counts toward `MaxModelCalls`; retries do not increment logical Agent iterations.
9. Backoff uses a cancellable timer and honors provider `RetryAfter`, bounded by the configured maximum when present.
10. `retry.scheduled` and `fallback.activated` events reference the completed failed Model Call.
11. Policy and adapter panics are contained as structured failures.

## Consequences

- Applications can recover from transient provider failures without adding a service dependency.
- Cost and latency remain bounded even for custom policies.
- Event consumers may see partial deltas from a failed attempt and must correlate data by Call ID.
- Failover across models with different context windows is not optimized until Context Manager exists.
- Distributed routing, health scoring, circuit breakers, hedged requests, and fleet-wide jitter remain adapter or Enterprise concerns.
- A caller that knows a Tool is idempotent may implement retry inside that Tool or a future explicit Tool policy; Core does not infer idempotency.

## Rejected Alternatives

- Always retry retryable errors: changes latency and cost without caller consent.
- Retry Tool errors marked retryable: `Retryable` currently means the model may try a corrected call, not that replaying the same external effect is safe.
- Put fallback routing in `tiggy-manage-agent`: prevents standalone embedded use and reverses the required dependency direction.
- Concurrent hedged Model calls: increases cost and complicates stream ordering and cancellation before a demonstrated requirement exists.
