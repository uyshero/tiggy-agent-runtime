# ADR 0014: Deterministic Context and Memory Fault Injection

- Status: Accepted
- Date: 2026-08-04

## Context

The Runtime contains panic and error tests, but ad hoc callbacks do not form a reusable fault model. Reliability evaluation needs repeatable injection at exact extension calls, deterministic cancellation synchronization, explicit invalid-result cases, and a way to model a delegate side effect followed by transport failure. A production fault switch would expand public API and risk accidental activation.

## Decision

1. Put fault injection only in `agenttest`; add no Agent Config field or production behavior.
2. Define five typed Points: Planner, Compressor, Token Counter, Memory Recall, and Memory Observe.
3. Target exact one-based per-Point call ordinals with immutable validated Rules.
4. Support pre-delegate error, panic, wait-for-cancel, invalid result, and error-after-successful-delegate Actions.
5. Reject invalid-result injection for Memory Observe because it has no result contract.
6. Add `Watch` synchronization so cancellation tests wait for boundary entry rather than sleeping.
7. Copy mutable request/result values across Wrappers and preserve caller ownership.
8. Make the Injector concurrency-safe while documenting that same-Point concurrent ordinal order follows lock acquisition.
9. Start no goroutines or timers, add no random behavior, and never retry a delegate.
10. Require integration tests to assert stable Agent errors, trace invariants, degradation, and delegate call/side-effect counts.

## Consequences

- Context and Memory recovery behavior can be evaluated with one reusable vocabulary.
- Cancellation tests no longer rely on timing sleeps.
- `error_after_delegate` makes ambiguous external effects visible and verifies no implicit replay.
- The harness tests Runtime contracts without pretending to reproduce infrastructure-level failure physics.
- Sharing one Injector across unrelated concurrent Runs makes ordinal assignment scheduling-dependent, so isolated scenario ownership remains necessary.

## Rejected Alternatives

- **Random failure probability:** produces flaky, irreproducible tests and weak failure localization.
- **Real sleep-based latency:** slows CI and creates scheduler-dependent cancellation races.
- **Fault flags in Agent Config:** exposes test authority in production API and risks accidental enablement.
- **Only callback test doubles:** possible but duplicates call counting, panic/error shapes, cancellation coordination, and ownership behavior.
- **Automatically retry after injected failure:** hides the exact no-replay behavior the harness must verify.
- **Simulate database/network/process faults in Core:** those require real Adapter/system environments and belong outside an embeddable SDK.
