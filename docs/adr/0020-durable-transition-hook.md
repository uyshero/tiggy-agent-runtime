# ADR 0020: Durable Transition Hook

- Status: Accepted
- Date: 2026-08-11

## Context

`v0.1.2` exposes complete State when an invocation returns. A hard process exit
during Model or Tool execution leaves a managed platform unable to distinguish
not-started, completed-but-uncommitted, and indeterminate external effects.
Lifecycle Events and Recorder journals are diagnostic evidence, not authority
to advance Agent State.

Putting PostgreSQL, queues, leases, tenants, or approval identities into Core
would reverse the dependency boundary. Keeping only return-time persistence
prevents a platform from building conservative crash recovery around Core.

## Decision

1. Add an optional synchronous `agent.TransitionHook` to `agent.Config`.
2. Emit versioned Model, frozen Tool batch, Tool effect, State commit, and
   continuation-consumption transitions with run-local sequence and stable IDs.
3. Treat Hook errors and panics as fail-closed `transition` RunErrors.
4. Give every Transition an owned State checkpoint and SHA-256 digest.
5. Keep ordinary JSON metadata content-free. Provide owned Model/Tool request
   and result values only through `json:"-"` private fields for encrypted storage.
6. Classify Tool effects explicitly. Default undeclared Tools to
   `non_reconcilable_write`.
7. Derive Tool idempotency keys from persisted call identity and digests so the
   key survives approval Resume, and inject it through Tool execution Context.
8. Do not add automatic Tool replay or an API that fabricates successful Tool
   Results from an operator assertion.

## Consequences

- A platform can transactionally checkpoint exact Runtime boundaries while
  keeping storage and scheduling dependencies outside Core.
- Hook latency is on the execution critical path. Implementations must bound
  I/O and be concurrency-safe.
- Private checkpoint data may contain secrets. The platform owns encryption,
  tenancy, retention, and access control.
- `tool_effect.started` without a terminal effect transition remains ambiguous.
  Effect class guides platform reconciliation but does not prove exactly-once.
- Omitting the Hook retains v0.1.2 behavior.

## Rejected Alternatives

- Use Event Sink or Recorder as the durable State transition authority.
- Persist only digests and discard completed Model/Tool results.
- Default unknown Tools to read-only or idempotent.
- Retry every Tool after a Worker crash.
- Make Core depend on an Enterprise database schema or Worker lease protocol.
