# Context Manager Semantics

## Token Budget Planning

`contextmanager.TokenPlanner` performs immutable preflight accounting before a model call. It measures the complete provider request through an injected `model.TokenCounter`, reserves output and a safety margin, and returns a validated Plan. Planning does not remove, summarize, reorder, or otherwise modify Messages or Tools.

```go
planner, err := contextmanager.NewTokenPlanner(contextmanager.TokenPlannerOptions{
    Counter: providerCounter,
    CountSource: contextmanager.CountSourceProvider,
    ContextWindowTokens: 128_000,
    SafetyMarginTokens: 1_024,
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    Name: "assistant",
    Model: provider,
    ModelIdentity: identity,
    ContextPlanner: planner,
    Limits: agent.Limits{
        // Other required limits omitted here.
        MaxOutputTokensPerCall: 4_096,
    },
})
```

The Counter sees owned copies of the Model identity, complete Message list, and Tool definitions. Therefore System/Skill instructions, conversation history, Tool Call/Result blocks, Tool schemas, and recalled Memory projection are included.

Core does not install a character-count heuristic. The application must inject a provider-aware Counter or an explicitly estimated Counter and set `CountSource` accordingly. `provider` means the application treats the Counter as provider-aware; Core does not independently certify tokenizer parity.

## Budget Invariants

For each Plan:

```text
available_input = context_window - output_reserve - safety_margin
required        = input + output_reserve + safety_margin
remaining       = max(available_input - input, 0)
overflow        = max(input - available_input, 0)
utilization     = input / available_input, in basis points
```

All counts are positive and bounded. `Plan.Validate` checks these equations, Model identity, source, thresholds, Fits, and Pressure. A custom Planner is accepted through the small `Planner` interface, but Agent validates its returned Plan again and rejects changes to Model identity or output reserve.

Default pressure thresholds are 80% Warning and 95% Critical. Pressure is diagnostic and does not itself remove context. Any positive overflow produces `PressureOverflow` and `Fits=false`.

The Agent uses `Limits.MaxOutputTokensPerCall` as output reserve. It normally plans once per Model target for one generation cycle: ordinary same-target retries reuse the unchanged Plan, a Fallback uses its own Planner/window, and the next ReAct iteration replans because Tool Results changed the context. A policy-authorized Provider context-length recovery is the bounded exception: the compressed recovery view is planned once before its retry. When planning is enabled, every Fallback must configure its own Planner.

## Runtime Behavior

Planning emits a balanced pair:

```text
context.prepare.started
context.prepared: ready | overflow | failed | canceled
```

The completed event records Model, count source, window, input, output reserve, safety margin, available/remaining/overflow tokens, utilization, pressure, and status. It does not record prompt or Tool-schema bodies.

Without a configured Compressor, overflow stops before `model.call.started` with `limit/context_overflow`. With one, the exact `OverflowTokens` in `context.prepared` becomes its measured reduction target. Counter/Planner errors terminate with `internal/context_planner_failed`; invalid custom Plans use `internal/invalid_context_plan`; cancellation returns `canceled/context_prepare_canceled`. Counter and custom Planner panics are contained.

## Structurally Safe Compression

An Agent may bind a `contextmanager.Compressor` beside each Context Planner. Compression normally runs after a validated overflow Plan. It may also run once after a policy-authorized Provider context-length rejection when the failed request had a validated fitting Plan. Core creates ordered inactive-history groups and passes owned copies to the Compressor:

```go
type Compressor interface {
    Compress(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error)
}
```

`CompressionRequest.Trigger` is `budget_overflow` or `provider_context_length`. The latter retains the honest fitting Plan and therefore records zero overflow; it means the Provider rejected a request that local preflight accepted, not that Core measured a deficit.

For compatibility, a missing Trigger in an older Compression Request or Event is interpreted only as the original `budget_overflow` path. An explicit `provider_context_length` Event must still be parented to its matching failed Model Call.

The Decision supplies a strategy name/version, a positive prefix count of eligible groups, and summary text. It never supplies the final Message list. Core uses `ApplyCompression` to replace the covered groups with one internal System summary carrying source Message IDs in Metadata. This prevents a Compressor from reordering protected messages or splitting a group.

Grouping and protection rules are deterministic:

- a User turn and its following Assistant/Tool messages form an ordered group;
- boundaries crossed by a Tool Call/Result relationship are merged;
- leading System messages, later System groups, the latest turn, and groups with unresolved Tool Calls are protected;
- only a contiguous inactive prefix is eligible;
- the full `Agent.State` is never rewritten; only the next provider request sees the summary view.

After one compression attempt, Agent plans the result again. A fitting budget-compression result proceeds to `model.call.started`; a fitting Provider-recovery result emits `retry.scheduled` and performs one same-target retry with a new Call ID. An insufficient result returns `limit/context_overflow` without another Provider call. Compressor errors and panics return `internal/context_compression_failed`, invalid Decisions return `internal/invalid_compression_result`, and cancellation returns `canceled/context_compression_canceled`. No eligible group returns `limit/context_compression_unavailable`.

Compression emits a balanced operation around the second planning pass:

```text
context.compression.started
context.compression.completed: completed | insufficient
context.compression.failed: failed | canceled
```

Outcomes record Model identity, before/after/target input tokens, measured overflow, strategy identity, covered group count, covered Message IDs, summary Message ID, and fallback. The only built-in fallback is `fail_closed`; Core does not install a lossy tail-truncation fallback.

“Semantics-preserving” here is a structural contract. Core can prove coverage, ordering, Tool relationships, and protected-work retention. It cannot prove that arbitrary summary text is factually faithful. Compressor implementations are caller-owned, must honor Context, and must be concurrency-safe when one Agent is reused concurrently.

`ContextPlanner=nil` preserves the pre-P3 behavior and sends requests without token preflight. Reliable production configurations should provide a Planner. Core cannot infer an accurate context window or tokenizer from a model name.

## Ownership and Non-goals

Planner requests and results are copied values. `TokenPlanner` is immutable and is concurrency-safe when its injected Counter is concurrency-safe. Context cancellation is propagated, but Core cannot forcibly preempt a Counter that ignores Context.

Core does not implement a model-backed summarizer, category-level token attribution, or prompt caching decisions. Provider context-length recovery is disabled by default and requires `TransientModelFailurePolicy.EnableContextRecovery`, a Planner, and a Compressor. Runtime permits it once per Model Target and counts the retried Provider request against `MaxModelCalls`. A Compressor that calls an external model owns that call and its resource/cost reporting; the current Core contract does not add the Compressor's own work to Agent `Usage` or `MaxModelCalls`.

Large Tool Result Artifact offload is implemented as a separate pre-projection boundary; see [Artifact offload semantics](./artifact.md).

The final content-free Request composition for each physical call is available on `model.call.started.context`; see [Context diagnostics](./context-diagnostics.md).
