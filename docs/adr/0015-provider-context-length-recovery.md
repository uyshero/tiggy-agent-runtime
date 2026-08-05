# ADR 0015: Bounded Provider Context-Length Recovery

- Status: Accepted
- Date: 2026-08-05

## Context

Provider-aware token planning is the primary context boundary, but tokenizer drift, hidden wire overhead, model aliases, or endpoint configuration changes can still make a Provider reject a request that passed local preflight. Repeating the same request cannot fix that failure, while unbounded recursive summarization compounds information loss and cost.

## Decision

1. Keep preflight planning and budget-triggered compression as the primary path.
2. A Provider Adapter must classify this rejection as `model.ErrorContextLength`.
3. Add the explicit `ModelFailureCompressRetry` action to `ModelFailurePolicy`; the default fail-fast policy never selects it.
4. `TransientModelFailurePolicy.EnableContextRecovery` opts into the action only when the current target has both a Planner and Compressor.
5. Runtime permits at most one context recovery per Model Target in one generation cycle, and the retry consumes `MaxModelCalls`.
6. Recovery compresses the exact request view rejected by the Provider, not full State. A Fallback target still begins from full State and uses its own context configuration.
7. `CompressionRequest.Trigger=provider_context_length` carries the validated fitting Plan honestly; Core does not invent an overflow count.
8. Core still constructs eligible semantic groups, applies the summary itself, and replans the compressed result before retrying.
9. Successful recovery emits paired Compression events followed by `retry.scheduled` with reason `context_length_recovery`; the Compression operation is parented to the failed Model Call completion.
10. A second context-length rejection, unavailable history, invalid compression, insufficient replanning, cancellation, or exhausted call budget terminates through the existing structured failure paths.
11. A missing Trigger in a legacy Compression Event is interpreted as `budget_overflow`; an explicit `provider_context_length` Trigger still requires a matching failed Model Call parent.

## Consequences

- Estimated counters and changing Provider endpoints can recover from one bounded mismatch without weakening structural compression guarantees.
- Recovery spends one failed Provider call and may spend Compressor resources, so it remains explicit and disabled by default.
- Event consumers can distinguish measured budget compression from Provider-triggered recovery without seeing prompt or summary bodies.
- Partial Deltas from the failed attempt remain correlated by Call ID; Core does not erase or merge attempt traces.
- This is request-view recovery, not durable execution replay and not a general retry for Tools or arbitrary Model failures.

## Rejected Alternatives

- **Always retry the same request:** cannot reduce context and spends another call predictably.
- **Enable recovery implicitly whenever a Compressor exists:** changes cost and latency without application consent.
- **Forge a positive overflow Plan:** makes diagnostics dishonest and violates Plan arithmetic.
- **Recursively compress until accepted:** has no safe information-loss or cost bound.
- **Rewrite full Agent State:** destroys source history and changes later continuation semantics.
