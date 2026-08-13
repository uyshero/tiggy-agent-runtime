# Orchestrator Enterprise Integration

This contract keeps `tiggy-agent-runtime` reusable while allowing
`tiggy-manage-agent` or another managed platform to run Brief executions
durably. The dependency direction is one way:

```text
tiggy-manage-agent/internal/runtimeadapter
  -> github.com/uyshero/tiggy-agent-runtime/orchestrator

tiggy-agent-runtime
  -X-> Enterprise packages, schemas, APIs, tenants, queues, or Workers
```

Core owns the portable execution protocol:

- strict Brief validation and deterministic task order;
- one isolated Subagent construction and one Artifact output per task;
- output verification and explicit input Artifact projection;
- opaque Artifact references, `ExecutionSnapshot`, checkpoint CAS, and
  `Resume` validation;
- content-free lifecycle Events and structured failures.

Enterprise owns operational authority:

- tenant and workspace authorization;
- execution records, queues, retry policy, and terminal-state transactions;
- Worker registration, capacity, polling, shutdown, and observability;
- lease acquisition, heartbeat, expiry, fencing token, and orphan recovery;
- durable Artifact/checkpoint adapters, encryption, retention, and cleanup;
- reconciliation of ambiguous external effects.

## Adapter Shape

Only the Enterprise `runtimeadapter` package should import Core. It constructs
Core values from Enterprise records and implements these injected Ports:

| Core Port | Enterprise adapter responsibility |
| --- | --- |
| `SubagentFactory` | Build one task-scoped Agent with the authorized Model, Toolkit, Memory scope, SafeSpace, credentials, and limits. |
| `Store` | Persist Artifact bytes under the authorized tenant/workspace; honor `PutRequest.IdempotencyKey`; return opaque references without signed URLs. |
| `CheckpointStore` | Load the authorized execution and atomically compare snapshot version plus the current fencing token before committing. |
| `Sink` | Persist or publish content-free lifecycle Events without treating them as recovery authority. |
| `agent.TransitionHook` | Persist fine-grained Agent boundaries and encrypted private recovery data when a task can execute Models or Tools. |

Lease data is deliberately absent from `ExecutionSnapshot`, `ArtifactReference`,
and Event payloads. An adapter instance should already be bound to the
Enterprise execution scope and lease claim. This prevents Core protocols from
becoming coupled to one database schema or tenancy model.

## Worker Flow

```text
claim queue item + lease epoch N
  -> create a cancelable execution Context
  -> construct lease-bound runtime adapters
  -> start heartbeat; cancel Context when renewal fails
  -> load Brief and choose Run or Resume
  -> execute Core Orchestrator
  -> commit terminal Enterprise state WHERE lease_epoch = N
  -> release lease and acknowledge queue item
```

The checkpoint adapter must reject every stale write, even if the worker's
Context has not yet observed cancellation. A typical SQL transaction checks all
of the following before updating the snapshot:

```text
execution_id matches
tenant/workspace scope is authorized
lease owner and fencing epoch are current
lease has not expired
checkpoint version equals expected
```

The Artifact adapter should apply the same authorization and, where supported,
fencing guard to `Put`. `Get` must authorize the opaque reference against the
bound execution scope. The stable per-task idempotency key allows a retry to
reuse an Artifact written before a failed checkpoint commit.

## Failure Semantics

- Heartbeat failure cancels the Core Context, but cancellation is cooperative;
  a Model, Tool, or Subagent that ignores Context may continue running.
- Checkpoint CAS is the authoritative progress gate. It prevents stale progress
  commits but does not prevent two workers from entering the same Subagent.
- Losing the lease must prevent checkpoint, terminal-state, and externally
  visible result commits. It must not be translated into successful completion.
- External Tool effects still require stable idempotency keys, lookup,
  compensation, or operator reconciliation. Neither a lease nor checkpoint CAS
  creates exactly-once effects.
- A completed checkpoint can be resumed without resolving Subagents. A partial
  checkpoint revalidates every Artifact before the next task starts.

## Repository Rules

Core must not contain Worker loops, SQL migrations, queue consumers, tenant
types, HTTP control-plane handlers, or Enterprise imports. Enterprise should
consume a tagged SemVer Runtime release through one adapter package rather than
copying Core code or relying on an adjacent source checkout. Contract tests in
Enterprise should cover stale fencing rejection, lease-loss cancellation,
duplicate delivery, Artifact idempotency, resume after process death, and
terminal commit conflicts.
