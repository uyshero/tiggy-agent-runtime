# Agent Runtime Semantics

## API and Ownership

- `agent.New` validates an immutable Agent configuration. One Agent may be reused concurrently when its injected Model and Tools support concurrent calls.
- `Run` starts with empty state. `Continue` accepts only complete state with no pending tool calls. `Resume` accepts only a validated approval Suspension and an exact set of decisions.
- Each run copies input state, model requests, Toolkit definitions, and returned values so retained slices cannot mutate an active run.
- The Toolkit is snapshotted at run start. Registrations made during a run affect later runs only.
- Activated Skills are immutable Agent configuration. Their instructions enter only the initial Instruction Stack System Message with `metadata.kind=instruction_stack`; `run.started` records name/version/digest provenance without content bodies.
- An optional per-model Context Planner runs before provider invocation. Overflow terminates before a Model Call; same-target retries reuse the unchanged Plan and Fallbacks use their own Planners.
- An optional per-model Context Compressor may replace only Core-selected inactive history groups in the provider request view. Full State remains unchanged.
- An optional Artifact Offloader runs after a successful Tool call and before its Result enters State. Offloaded State contains a digest-bound reference; the complete Result remains in the injected Store.
- An optional Memory Port recalls once before model execution and observes once after successful response commit. Recalled evidence changes only the model request view; observation receives only messages created in the current turn.
- `Result.State` is an independent snapshot for application-managed `Continue` or `Resume`; serialization alone does not provide durable scheduling.

## Execution and Failure

The runtime executes model and tool calls sequentially until completion, cancellation, failure, or a configured limit. Current limits cover iterations, model calls, tool calls, cumulative tokens, per-call output tokens, per-call streamed Delta count/bytes, and total duration.

The total duration is propagated to model and tool calls through Context. The Event Sink is caller code and must return promptly or implement bounded I/O; the runtime cannot preempt a callback that blocks without returning.

`MaxModelDeltasPerCall` and `MaxModelDeltaBytesPerCall` are checked before a Delta enters the Event Sink or `Result.Events`; the byte budget counts the UTF-8 bytes in all variable-length Delta string fields without copying their bodies. Fixed event-envelope overhead is bounded by the Delta count. Exceeding either budget fails the Run with `limit/max_model_deltas` or `limit/max_model_delta_bytes`. The first failure is latched, so a broken Provider Adapter that ignores the DeltaSink error cannot keep growing the diagnostic event trace. It may still occupy its own `Generate` call until it returns or honors Context cancellation. Runtime serializes concurrent callbacks and closes the Delta entry point when `Generate` returns, preventing late callbacks from changing the event sequence. Zero values for these two fields receive defaults so explicit `Limits` written before the fields existed remain valid; negative values are rejected.

Tool failures have two classes:

- `tool.Error` is sanitized and model-actionable. It becomes `ToolResult(IsError=true)` and execution continues.
- ordinary errors, panics, and invalid tool results are runtime failures. The runtime appends a safe interrupted Tool Result and terminates.

Completed, failed, and canceled paths close pending tool calls. A
`StatusSuspended` result intentionally retains one digest-bound pending Tool
batch and terminates the current invocation with `run.suspended`; see
[Tool approval and resume semantics](./approval.md).

An optional `ApprovalPolicy` evaluates the complete model-produced Tool batch
before any call in that batch executes. A required decision suspends the whole
batch with zero batch side effects. Resume validates the argument and Tool
definition digests, applies every required decision exactly once in original
order, and does not call the Policy again. Rejection becomes a model-visible
Tool Error rather than a Runtime failure.

Artifact offload applies only to successful Tool Results. Safe model-actionable Tool errors remain inline. A configured Offloader receives an owned, versioned payload and is invoked at most once per Result; Store/Offloader failures, invalid Decisions, payload limits, and cancellation never fall back to silently inlining a result that violated the configured boundary. The runtime appends an explicit error Tool Result and terminates. Because cancellation can race with a completed Put, the Store may contain an unreferenced object; Core does not issue an automatic Delete.

`tool.result.offload.started` is paired with `tool.result.offload.completed` (`inline` or `offloaded`) or `tool.result.offload.failed` (`failed` or `canceled`). Events record size, media type, digest, opaque Object Reference, and fallback without result bodies.

Memory Recall/Observe operations receive owned values and the Run Context. Ordinary Adapter failures degrade by default and may be configured as `fail_run`. Cancellation always cancels the Run; panic, invalid/cross-scope recall results, and recall-limit violations are fatal. Agent filters expired recalled Items before every model projection. Observe starts only after the final Assistant response and its completed Model Call event; a degraded Observe preserves the completed Result. Memory events expose active/expired counts and Namespace but no text, observed messages, expiry, provenance correlation, or Scope key.

Every new `model.call.started` Event carries an optional content-free Context Manifest for the exact physical Provider Request. It records fixed aggregate Message/Role/Visibility/projection/Content/Tool Definition counts and bytes, including pre-compression and final Message counts. Older Protocol V1 Events may omit it. See [Context diagnostics](./context-diagnostics.md).

## Model Retry and Fallback

The default failure policy is fail-fast and never retries implicitly. Applications that need transient-failure recovery can configure `TransientModelFailurePolicy` and ordered `Fallbacks`:

```go
agent.Config{
    Model: primary,
    ModelIdentity: primaryID,
    Fallbacks: []agent.ModelTarget{{Model: backup, Identity: backupID}},
    FailurePolicy: agent.TransientModelFailurePolicy{
        MaxAttemptsPerTarget: 2,
        BaseDelay: 200 * time.Millisecond,
        MaxDelay: 2 * time.Second,
        EnableFallback: true,
        EnableContextRecovery: true,
    },
}
```

- The built-in policy retries ordinary failures only when the Provider Adapter explicitly sets `ProviderError.Retryable=true`.
- `EnableContextRecovery` is a separate opt-in: `ErrorContextLength` may trigger one structurally safe compression and same-target retry when that target has both a Planner and Compressor.
- A retry uses the same state and model target with a new Call ID. A fallback moves to the next explicitly configured model target.
- Every physical attempt counts toward `MaxModelCalls`, so a custom policy cannot retry without bound.
- Backoff responds to Context cancellation and stops and drains its timer.
- `retry.scheduled` and `fallback.activated` record the failed call, target, reason, and delay.
- A model may emit deltas before failing, so consumers must correlate attempts by Call ID.
- A Provider Adapter must stop streaming when DeltaSink returns an error; Core still bounds retained Delta events when an Adapter violates this contract.
- Tools are not retried automatically because the runtime cannot assume external side effects are repeatable.

A custom `ModelFailurePolicy` must be concurrency-safe. Invalid decisions and panics are contained as `model_failure_policy_failed` without corrupting state.

Panics from Model, Tool, Memory, failure-policy, and Event Sink boundaries become structured failures. Injected Ports are long-lived dependencies and are not closed by the runtime. Adapters must release per-call response bodies, streams, and temporary resources before returning and must honor Context cancellation.

Context planning uses the same cancellation boundary. Counter/Planner failures are run-fatal because proceeding would invalidate the configured context guarantee. `context.prepare.started/prepared` are balanced on ready, overflow, error, and cancellation paths.

On overflow, a configured Compressor gets one attempt, followed by mandatory replanning. Ordinary same-target retries reuse the compressed view; Fallback targets restart from full State with their own Planner and optional Compressor. When explicitly enabled, a Provider context-length rejection that escaped preflight may compress the failed request view once, replan it, and consume one more `MaxModelCalls` slot. A second rejection is not compressed again. Compression errors, invalid Decisions, cancellation, insufficient reduction, and Event Sink failures are fail-closed and emit a balanced `context.compression.started/completed|failed` operation before termination.

## Event Sink

The Sink consumes strictly ordered events synchronously. Returning an error means the consumer cannot continue; the runtime stops and sends no further events to that Sink. `Result.Events` retains the runtime's completed diagnostic trace with paired operation events and exactly one terminal event, including `run.suspended`.

Sink failure semantics:

- failure before the terminal event returns `Result.Status=failed` and `event_sink/event_sink_failed`;
- if execution completed but the Sink rejects the terminal event, the actual execution status and Final are preserved while `event_sink/terminal_event_failed` is returned.

The second case separates Agent execution failure from result-delivery failure. Callers must inspect both Status and the returned error.

The optional `observe` package composes read-only consumers over this Sink boundary. Direct Sinks remain strict. `observe.BestEffort` contains observer errors and panics when logging or telemetry must not fail execution; it does not make a blocking callback asynchronous. See [Observation semantics](./observe.md).

The optional `record.Recorder` is another strict Sink consumer. It writes each accepted Event incrementally, so a Journal can retain a valid partial Run when execution or the process stops before a terminal Event. Recording failure follows the same Sink rules and never triggers an automatic append retry. See [Run Recorder semantics](./record.md).

## Current Non-goals

State and events provide in-process correctness and diagnostics. Approval State
is serializable for application-managed persistence, but Core does not promise
crash recovery, single-resumer coordination, cross-process replay, exactly-once
external side effects, or a database-backed durable state machine. For one
possible database and Worker design, see
[Enterprise Runtime persistence reference](./enterprise-runtime.md).
