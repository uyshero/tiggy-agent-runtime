# Durable Transition Hook

`agent.Config.TransitionHook` gives managed platforms a database-neutral,
synchronous, fail-closed checkpoint protocol. It closes the gap where complete
State was visible only after an invocation returned, without adding a database,
queue, lease, tenant, or approver model to Core.

The Hook is optional. Omitting it preserves the existing in-process behavior of
`Run`, `Continue`, and `Resume`; `Recover` requires a configured Hook.

## Transition Types

Each invocation has an independent run-local sequence and stable Transition IDs:

| Type | Durable boundary |
| --- | --- |
| `model_call.prepared` | The provider request is frozen but Model has not been called. |
| `model_call.completed` | Model returned; private data may contain the final Response. |
| `tool_batch.frozen` | Tool calls, argument/definition digests, effect classes, and idempotency keys are frozen. |
| `tool_effect.prepared` | One Tool effect is prepared but has not started. |
| `tool_effect.started` | Runtime is about to call the Tool. A missing terminal record makes recovery indeterminate. |
| `tool_effect.completed` | Tool returned a known State-ready result; private data contains the exact projected Result when recoverable. |
| `tool_effect.indeterminate` | Cancellation, runtime failure, or unpersisted completion left the external outcome uncertain. |
| `state_transition.committed` | Candidate State is valid; Runtime replaces in-memory State only after Hook success. |
| `resume_continuation.consumed` | Approval decisions are exact; Suspension is cleared only after Hook success. |
| `recovery_continuation.applied` | A saved completed result has been validated and applied to candidate State before execution continues. |

`Transition.StateDigest` is the SHA-256 of the complete `Checkpoint`.
`Transition.Validate` recomputes it and rejects mismatches.

## Configuration

```go
value, err := agent.New(agent.Config{
    Name: "managed-agent",
    Model: provider,
    ModelIdentity: model.Identity{Provider: "openai", Model: "gpt"},
    Toolkit: kit,
    TransitionHook: checkpointStore,
    ToolEffectClasses: map[string]agent.ToolEffectClass{
        "crm.lookup": agent.ToolEffectReadOnly,
        "ticket.upsert": agent.ToolEffectIdempotentWrite,
        "payment.capture": agent.ToolEffectReconcilableWrite,
    },
})
```

Undeclared Tools default to `non_reconcilable_write`. The other classes are
`read_only`, `idempotent_write`, and `reconcilable_write`.

Hook implementations must be concurrency-safe and bound their own database or
network I/O. An error or panic becomes a `transition/*` RunError and prevents
the next Model or Tool effect. There is no best-effort mode.

## Metadata and Private Data

Transition JSON contains only IDs, sequence, digests, effect class, outcome,
and idempotency keys. Prompt, Tool arguments, and result bodies are absent.

`Transition.Checkpoint` and `Transition.Private` use `json:"-"`:

- `Checkpoint` is an owned complete `agent.State` copy;
- `Private.ModelRequest` and `ModelResponse` carry encrypted model recovery data;
- `Private.ToolArguments` and `ToolResult` carry encrypted Tool recovery data;
- `Private.ToolResultStateReady`, `ToolResultIsError`, and `ToolResultRetryable`
  preserve private State-projection semantics and must be stored with the Result.

Platforms must tenant-isolate and encrypt these values. Avoid reflection-based
or generic structural logging of the complete Go value; `json.Marshal` emits
only the content-free metadata.

## Tool Idempotency

A Tool effect key is deterministically derived from the source Assistant
Message ID, Call ID, Tool definition digest, argument digest, and effect class.
It remains stable across approval suspension and a later Resume invocation.

The same metadata is injected into Tool execution context:

```go
metadata, ok := agent.ToolTransitionFromContext(ctx)
if ok {
    request.IdempotencyKey = metadata.IdempotencyKey
}
```

Direct `toolkit.View.Call` use bypasses Agent and has no such context value.

## Recovery API

The platform selects its latest durable boundary and passes an owned completed
Transition back to Core. It must not construct the next `agent.State` itself:

```go
result, err := value.Recover(ctx, completedTransition, sink)
```

`Recover` accepts only `model_call.completed` with outcome `completed`, or
`tool_effect.completed` with outcome `succeeded` / `tool_error` and an exact
private result. It revalidates the checkpoint digest, Model response semantics,
Tool call order, arguments, current Tool definition, effect class, idempotency
key, result structure, and usage limits.

Before another Model or Tool effect, Core synchronously commits
`recovery_continuation.applied`. Its idempotency key is stable for the source
Transition. The Hook must enforce one logical application and reject or
idempotently accept duplicates only when their State digest is identical.

A recovered Model response continues through the normal approval and Tool
pipeline. A recovered Tool result continues to the next Model when it closes
the batch. If calls remain in that batch, Core creates a new approval
Suspension for every remaining call; it does not silently re-evaluate them into
automatic execution.

## Recovery Rules

- `tool_effect.prepared` means the effect did not start;
- `tool_effect.started` must not be replayed automatically unless external
  semantics prove that replay is safe;
- completed Model/Tool result without State commit can be passed to `Recover`;
- indeterminate read-only/idempotent effects may follow platform policy,
  reconcilable writes require external lookup, and non-reconcilable writes
  require operator handling;
- lease, fencing, or optimistic versions must reject duplicate Workers;
- continuation consumption is keyed by Suspension ID and must have one winner.

Core never automatically replays Tools, fabricates a Tool Result from an
operator assertion, selects which stored boundary is newest, coordinates
Workers, or promises exactly-once external effects. A v0.2.0 Tool completion
created with Artifact Offload is not automatically recoverable because its
private result predates the State-ready projection boundary.

See [ADR 0020](./adr/0020-durable-transition-hook.md),
[ADR 0021](./adr/0021-recovery-continuation-api.md), and the
[Enterprise persistence reference](./enterprise-runtime.md).
