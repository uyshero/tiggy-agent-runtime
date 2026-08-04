# ADR 0006: Explicit Token Budget Planning Before Model Calls

- Status: Accepted
- Date: 2026-08-03

## Context

Reliable compression requires a measured deficit, not a provider error or an arbitrary message-count threshold. Context accounting must include Messages and Tool schemas, reserve output before selecting history, support different Fallback windows, expose uncertainty, and never silently trim valid protocol groups. Tokenizers remain provider-specific, so Core cannot infer an accurate counter from a model name.

## Decision

1. `contextmanager.TokenPlanner` receives an injected `model.TokenCounter`, explicit count source, context window, safety margin, and pressure thresholds.
2. Agent supplies the complete Message/Tool request and uses `MaxOutputTokensPerCall` as output reserve.
3. A Plan contains bounded counts, available/required/remaining/overflow arithmetic, utilization basis points, pressure, and Fits. `Plan.Validate` makes these equations a public contract.
4. Core provides no default character heuristic. Estimated Counters must be injected and labeled `estimated`; provider-aware Counters are labeled `provider` by the application.
5. Agent validates custom Planner output again, including Model identity and output reserve. Counter and Planner panics are contained.
6. Planning runs once per Model target for a generation cycle. Same-target retries reuse the unchanged Plan; Fallback targets require their own Planner; later ReAct iterations replan.
7. Planning emits paired `context.prepare.started` and `context.prepared` events without prompt bodies.
8. Overflow stops before provider invocation with `limit/context_overflow`. This stage does not truncate or compress context; a later policy consumes the measured deficit.
9. Planner configuration remains optional before v1 for compatibility. Applications requiring reliable context control must configure it explicitly.

## Consequences

- Planning is deterministic relative to the injected Counter and adds no module dependency, goroutine, network client, or stored Context.
- Provider adapters own tokenizer accuracy and Counter concurrency. Core owns validation, copying, error classification, and event semantics.
- Fallback configuration becomes more explicit because each model may have a different tokenizer and window.
- A missing Planner can still lead to a provider context-length error; the Runtime does not claim preflight safety in that mode.
- Exact per-category attribution is not derived by multiple independent Counter calls because tokenizer request overhead is not necessarily additive.

## Rejected Alternatives

- Use character or byte length silently: language/model variance can undercount and creates false precision.
- Wait for provider context-length errors: wastes calls, weakens diagnostics, and cannot measure a compression target before failure.
- Subtract output reserve after selecting history: can produce requests that fit input but leave no valid completion capacity.
- Reuse the primary Plan for every Fallback: context windows and tokenizers differ.
- Automatically drop oldest messages on overflow: can break Tool Call/Result invariants and loses context without provenance.
