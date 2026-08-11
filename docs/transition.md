# Durable Transition Hook

`agent.Config.TransitionHook` gives managed platforms a database-neutral,
synchronous, fail-closed checkpoint protocol. It closes the gap where complete
State was visible only after an invocation returned, without adding a database,
queue, lease, tenant, or approver model to Core.

The Hook is optional. Omitting it preserves the existing in-process behavior of
`Run`, `Continue`, and `Resume`.

## Transition Types

Each invocation has an independent run-local sequence and stable Transition IDs:

| Type | Durable boundary |
| --- | --- |
| `model_call.prepared` | The provider request is frozen but Model has not been called. |
| `model_call.completed` | Model returned; private data may contain the final Response. |
| `tool_batch.frozen` | Tool calls, argument/definition digests, effect classes, and idempotency keys are frozen. |
| `tool_effect.prepared` | One Tool effect is prepared but has not started. |
| `tool_effect.started` | Runtime is about to call the Tool. A missing terminal record makes recovery indeterminate. |
| `tool_effect.completed` | Tool returned a known result; private data may contain the complete Result. |
| `tool_effect.indeterminate` | Cancellation, runtime failure, or unpersisted completion left the external outcome uncertain. |
| `state_transition.committed` | Candidate State is valid; Runtime replaces in-memory State only after Hook success. |
| `resume_continuation.consumed` | Approval decisions are exact; Suspension is cleared only after Hook success. |

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
- `Private.ToolArguments` and `ToolResult` carry encrypted Tool recovery data.

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

## Recovery Rules

- `tool_effect.prepared` means the effect did not start;
- `tool_effect.started` must not be replayed automatically unless external
  semantics prove that replay is safe;
- completed effect without State commit can use the encrypted Result to finish
  the State transition;
- indeterminate read-only/idempotent effects may follow platform policy,
  reconcilable writes require external lookup, and non-reconcilable writes
  require operator handling;
- lease, fencing, or optimistic versions must reject duplicate Workers;
- continuation consumption is keyed by Suspension ID and must have one winner.

Core never automatically replays Tools, fabricates a Tool Result from an
operator assertion, or promises exactly-once external effects.

See [ADR 0020](./adr/0020-durable-transition-hook.md) and the
[Enterprise persistence reference](./enterprise-runtime.md).
