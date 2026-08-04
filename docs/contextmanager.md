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

The Agent uses `Limits.MaxOutputTokensPerCall` as output reserve. It plans once per Model target for one generation cycle: same-target retries reuse the unchanged Plan, a Fallback uses its own Planner/window, and the next ReAct iteration replans because Tool Results changed the context. When planning is enabled, every Fallback must configure its own Planner.

## Runtime Behavior

Planning emits a balanced pair:

```text
context.prepare.started
context.prepared: ready | overflow | failed | canceled
```

The completed event records Model, count source, window, input, output reserve, safety margin, available/remaining/overflow tokens, utilization, pressure, and status. It does not record prompt or Tool-schema bodies.

Without a configured Compressor, overflow stops before `model.call.started` with `limit/context_overflow`. With one, the exact `OverflowTokens` in `context.prepared` becomes its measured reduction target. Counter/Planner errors terminate with `internal/context_planner_failed`; invalid custom Plans use `internal/invalid_context_plan`; cancellation returns `canceled/context_prepare_canceled`. Counter and custom Planner panics are contained.

## Structurally Safe Compression

An Agent may bind a `contextmanager.Compressor` beside each Context Planner. Compression runs only after a validated overflow Plan. Core creates ordered inactive-history groups and passes owned copies to the Compressor:

```go
type Compressor interface {
    Compress(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error)
}
```

The Decision supplies a strategy name/version, a positive prefix count of eligible groups, and summary text. It never supplies the final Message list. Core uses `ApplyCompression` to replace the covered groups with one internal System summary carrying source Message IDs in Metadata. This prevents a Compressor from reordering protected messages or splitting a group.

Grouping and protection rules are deterministic:

- a User turn and its following Assistant/Tool messages form an ordered group;
- boundaries crossed by a Tool Call/Result relationship are merged;
- leading System messages, later System groups, the latest turn, and groups with unresolved Tool Calls are protected;
- only a contiguous inactive prefix is eligible;
- the full `Agent.State` is never rewritten; only the next provider request sees the summary view.

After one compression attempt, Agent plans the result again. A fitting result proceeds to `model.call.started`; an insufficient result returns `limit/context_overflow` without invoking the provider. Compressor errors and panics return `internal/context_compression_failed`, invalid Decisions return `internal/invalid_compression_result`, and cancellation returns `canceled/context_compression_canceled`. No eligible group returns `limit/context_compression_unavailable`.

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

This stage does not implement a model-backed summarizer, provider context-length retry, category-level token attribution, or prompt caching decisions. A Compressor that calls an external model owns that call and its resource/cost reporting; the current Core contract does not add it to Agent `Usage` or `MaxModelCalls`.

Large Tool Result Artifact offload is implemented as a separate pre-projection boundary; see [Artifact offload semantics](./artifact.md).
