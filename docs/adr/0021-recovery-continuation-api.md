# ADR 0021: Recovery Continuation API

- Status: Accepted
- Date: 2026-08-12

## Context

ADR 0020 added durable observation boundaries and private Model/Tool results.
It did not expose a Core-owned operation that could validate a saved completed
result, construct the next State, and continue execution. A managed platform
could classify a crash but had to mutate `agent.State` directly to advance a
completed-but-uncommitted result. That bypassed Runtime invariants and left the
recovery claim incomplete.

## Decision

1. Add `Agent.Recover(ctx, completedTransition, sink)` as the only public entry
   point for applying a completed Model or Tool result.
2. Accept only deterministic completion outcomes with saved private data.
   Prepared, started, failed, canceled, and indeterminate transitions are not
   result continuations.
3. Revalidate the source Transition, checkpoint digest, Model semantics, Tool
   order, arguments, definition digest, effect class, idempotency key, result,
   and usage limits before changing State.
4. Persist `recovery_continuation.applied` with a stable source-derived
   idempotency key before another external effect.
5. Normalize Model responses before their completed checkpoint. Persist only
   State-ready successful Tool Results after optional Artifact projection.
6. Preserve exact safe Tool-error content and error/retry flags in private
   recovery data.
7. Continue a recovered Model response through the normal Tool policy. After a
   recovered Tool result, place any remaining calls from the old batch behind
   a fresh approval Suspension.
8. Require a Transition Hook. Recovery without durable commit authority is
   rejected.

## Consequences

- Platforms no longer need to construct Tool or Assistant messages inside
  `agent.State`.
- A completed result can cross a process boundary without replaying its Model
  or Tool call.
- The platform still chooses the latest durable boundary and owns encryption,
  leases, fencing, uniqueness, scheduling, and duplicate-Worker prevention.
- Hook implementations must enforce one logical recovery application per
  stable idempotency key. An identical retry may be accepted idempotently;
  mismatched State digests must be rejected.
- v0.2.0 Tool completions created with Artifact Offload are not eligible for
  automatic result application because their saved private value is not the
  final projected State value.
- Recovery does not make indeterminate effects safe and does not provide
  exactly-once external execution.

## Rejected Alternatives

- Export internal State append helpers.
- Accept a caller-constructed next State.
- Treat an operator assertion as a successful Tool Result.
- Replay every started or indeterminate Tool.
- Re-evaluate remaining calls from a partially completed Tool batch and allow
  them to execute without another approval boundary.
