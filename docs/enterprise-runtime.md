# Enterprise Runtime Persistence Reference

This document describes how an Enterprise service can wrap `agent.State`,
`approval.Suspension`, Durable Transitions, and Runtime Events in a
database-backed Agent execution state machine. Database and Worker design remain
an application reference; the optional Transition Hook itself is a Core API.

Core provides process-local correctness only:

- `Run`, `Continue`, and `Resume` are single function invocations;
- `Result.State` is a serializable continuation value;
- `StatusSuspended` means the current invocation terminated safely and returned
  an approval continuation point;
- optional `TransitionHook` synchronously delivers encryptable checkpoints at
  Model, Tool effect, State commit, and Resume boundaries;
- Events and Recorder entries support diagnostics, but do not own replay or
  recovery authority.

Enterprise must add persistence, scheduling, approval identity, tenancy, audit,
Worker coordination, and crash recovery.

## Recommended State Machine

```text
queued
  -> running
     -> suspended
        -> resuming
           -> running
     -> completed
     -> failed
     -> canceled

running/resuming
  -> retry_waiting
  -> failed
```

Core rules:

- `running` and `resuming` must be protected by a Worker lease or optimistic
  version;
- `suspended` consumes no Runtime goroutine and no Worker;
- after approval, do not execute Tools directly; first atomically move the
  database row from `suspended` to `resuming`;
- `completed`, `failed`, and `canceled` are business task terminal states;
- `run.suspended` is terminal for one Runtime invocation, not for the business
  task.

## Minimal Data Model

This is a conceptual model. Core must not import these types.

| Table/record | Key fields | Responsibility |
| --- | --- | --- |
| `agent_runs` | `id`, `tenant_id`, `status`, `state_json`, `suspension_json`, `runtime_version`, `toolkit_digest`, `version`, `lease_owner`, `lease_until` | Stores the current task state, continuation value, concurrency control, and recovery entry point. |
| `approval_items` | `run_id`, `approval_id`, `call_id`, `tool`, `arguments_digest`, `tool_digest`, `status`, `decided_by`, `decided_at`, `reason` | Stores Enterprise approval records, approver identity, timing, and audit fields. |
| `run_events` | `run_id`, `sequence`, `event_type`, `event_json`, `created_at`, `redaction_version` | Stores a queryable timeline; it may ingest Observer output or Recorder journal entries. |
| `tool_effects` | `run_id`, `call_id`, `tool`, `status`, `started_at`, `finished_at`, `idempotency_key`, `external_ref` | Tracks external side effects for crash reconciliation, lookup, compensation, or human intervention. |
| `runtime_transitions` | `run_id`, `transition_id`, `sequence`, `type`, `state_digest`, `metadata_json`, `encrypted_checkpoint`, `encrypted_private_data` | Atomically stores content-free metadata and encrypted recovery material; both Transition ID and run-local sequence are unique. |

`state_json`, `suspension_json`, and Transition private data must be encrypted
and tenant-isolated because they may contain conversation content, complete Tool
arguments and results, message IDs, Artifact references, and model-visible
context structure.

## Persisting a Suspension

```text
Worker owns lease
  -> calls runtime.Run / Continue
  -> receives StatusSuspended
  -> within one DB transaction:
       update agent_runs.status = suspended
       store Result.State
       store Result.Suspension
       insert approval_items for required calls
       append terminal run.suspended event
       clear lease
  -> notify approval system
```

Notify the approval system after the database transaction commits. Otherwise a
human may receive a notification while the service has not persisted a reliable
continuation point.

## Approval Resume Flow

```text
Approver submits decisions
  -> verify identity, tenant, policy, expiry
  -> transactionally mark approval_items decided
  -> atomically move agent_runs suspended -> resuming
       where version matches and all required approvals are decided
  -> enqueue Worker

Worker owns lease
  -> loads state_json and decisions
  -> calls runtime.Resume
     -> StatusSuspended with error: keep DB suspended or operator_required
     -> StatusCompleted: store final State and complete
     -> StatusFailed/StatusCanceled: store terminal status
```

If `Resume` returns `approval_toolkit_mismatch` or
`invalid_approval_resolution`, do not discard the old `State` / `Suspension`.
The resume input violated Core invariants, so the application should preserve
the original continuation point and enter a correctable state.

## Crash-Recovery Boundaries

Enterprise cannot claim durable execution by storing only `Result.State`. With
`TransitionHook` enabled, it should atomically store the Transition, task
version, and Tool-effect index and cover at least these crash points:

- before and after a model request;
- before Tool start, during Tool execution, after Tool return, and before Tool
  result persistence;
- after `run.suspended` returns and before approval records commit;
- after approval decisions persist and before Worker wake-up;
- after a Worker obtains a lease and before it commits progress;
- current Toolkit mismatch with the suspended digest;
- Recorder or Observer write failure.

For external side effects, prefer Tool adapters that accept an Enterprise
generated `idempotency_key` and persist an `external_ref` when the external
system is queryable. If the external system supports neither idempotency nor
lookup, crash recovery should require human reconciliation instead of automatic
Tool replay.

## Division of Responsibility

| Capability | Core | Enterprise |
| --- | --- | --- |
| Single-invocation state validation | Yes | Calls it and handles results |
| Serializable `State` / `Suspension` | Yes | Encrypts, persists, versions |
| Synchronous Transition checkpoint | Optional Hook | Transaction, encryption, uniqueness, recovery decision |
| Zero-side-effect approval suspension | Yes | Approver, notification, expiry, audit |
| Single-resumer coordination | No | DB version, lease, queue |
| Crash recovery | No | Worker recovery and effect reconciliation |
| Exactly-once external side effects | No | Idempotency keys, lookup, compensation, or human intervention |
| Timeline query | Emits Events | Persistence, indexes, permissions, UI |

See [Durable Transition Hook](./transition.md) for ordering, private payloads,
and effect classes. In short: Core exposes verifiable execution boundaries;
Enterprise ensures the task survives across processes, people, and time.
