# Changelog

All compatibility-relevant changes to public Go APIs, Message/Event protocols, error classes, and terminal reasons are recorded here before release.

## Unreleased

## [0.2.1] - 2026-08-12

### Added

- `Agent.Recover` validates a durably saved `model_call.completed` or `tool_effect.completed` Transition, applies its exact private Model/Tool result through Core-owned State construction, persists `recovery_continuation.applied`, and continues without caller mutation of `agent.State`.
- Recovery rejects changed State digests, Tool definitions, arguments, effect classes, idempotency keys, invalid results, indeterminate effects, and missing Hooks before another Model or Tool effect can start.
- Recovered multi-Tool batches place all remaining unstarted calls behind a new approval Suspension instead of re-evaluating them into automatic execution.

### Changed

- Successful `tool_effect.completed` checkpoints now contain the final State-ready inline or Artifact-projected Tool Result. Model responses are normalized before their completed checkpoint is persisted, making repeated recovery deterministic.
- Model-visible Tool errors persist their exact safe Result and error/retry flags for recovery. Artifact projection failures remain indeterminate and are not eligible for automatic result application.

### Compatibility

- `Agent.Recover`, `TransitionRecoveryContinuationApplied`, and private recovery flags are additive. Existing callers that do not invoke recovery keep v0.2.0 behavior.
- Successful v0.2.0 Tool completions remain recoverable when no Artifact Offloader is configured. A legacy completion from an Agent with Artifact Offload is rejected because its private value predates the State-ready projection boundary.
- Core still does not replay indeterminate Tools, accept operator assertions as results, select the latest durable boundary, provide Worker leases/fencing, or promise exactly-once external effects.

### Release Notes

- This pre-v1 patch completes the recovery surface introduced by v0.2.0 without changing existing `Run`, `Continue`, or `Resume` callers. Go 1.25.12 or later remains required.
- Applications must persist the complete private recovery fields, enforce recovery idempotency keys, and route only the latest completed boundary to `Recover`.
- Rollback to v0.2.0 disables `Agent.Recover`; Runs that have emitted `recovery_continuation.applied` must remain on a v0.2.1-compatible Executor. Repository maintainers own tag revocation and security replacement releases.

## [0.2.0] - 2026-08-11

### Added

- Optional synchronous, fail-closed `agent.TransitionHook` checkpoints `model_call.prepared/completed`, frozen Tool batches, Tool effect prepared/started/completed/indeterminate boundaries, validated State commits, and approval continuation consumption without importing Enterprise storage or scheduling types.
- `agent.State.Digest` binds each owned checkpoint to a stable SHA-256 value; Transition JSON remains content-free while `json:"-"` private fields carry owned Model/Tool requests and results for platform encryption.
- Tool effect classes default conservatively to `non_reconcilable_write`; deterministic effect idempotency keys survive approval Resume and are available to Tool adapters through `agent.ToolTransitionFromContext`.

### Compatibility

- `Config.TransitionHook` and `Config.ToolEffectClasses` are additive and optional. Omitting them preserves v0.1.2 execution behavior.
- `approval.PendingCall` gains additive optional `effect_class` and `idempotency_key` fields. New Suspensions freeze them; v0.1.2 Suspensions with both fields absent remain resumable.
- Transition failures add the stable `transition` RunError class and stop execution before the next Model or Tool effect.

### Release Notes

- This remains a pre-v1 module. v0.2.0 is source-compatible for callers that omit the new options, while future minor versions may still change public contracts before v1.0.0.
- Go 1.25.12 or later is required.
- Core does not provide durable storage, Worker leasing, encryption, or exactly-once external effects. Embedding applications own Transition persistence and reconciliation; interrupted non-reconcilable writes remain indeterminate and require operator handling.
- Application owners can roll back by pinning v0.1.2 and reverting their Transition adapter. Repository maintainers own tag revocation and any security replacement release.

## [0.1.2] - 2026-08-11

### Added

- `observe.AsyncSink` provides an official bounded asynchronous Sink wrapper for operational telemetry, with configurable queue size, drop-newest/drop-oldest/block overflow policies, optional immediate-availability `BatchSink` coalescing, error diagnostics, graceful `Flush`/`Shutdown`, non-waiting queue-discarding `Abort`, a content-free runtime statistics snapshot, and application-scheduled `AsyncMetrics` Gauge/Counter-delta export through the existing Recorder and OTel contracts while preserving direct synchronous Sinks for strict audit and recording.
- AsyncSink stress accounting and benchmark coverage now provide a repeatable local baseline for direct, asynchronous, batched, and parallel observer delivery.
- Static progressive capability disclosure hardening now covers zero, single-Group, and multi-Group Toolkit projections, immutable per-Run Tool views, an external consumer example that asserts the exact model-visible Tool set, and Retry/Fallback scenarios that must not silently expand Tool exposure.
- `toolkit.View.DefinitionBytes()` reports a content-free aggregate byte count for the Tool definitions visible in one immutable Toolkit view, enabling schema-budget diagnostics without rewriting or summarizing JSON Schema bodies.
- Context Assembly now renders a model-visible Instruction Stack with a fixed dynamic-context boundary, static segment kinds, and content-free per-kind byte diagnostics for `system_prompt`, `project`, `task_rule`, `policy`, `knowledge`, and `skill`.
- `agent.Config.Instructions` lets applications inject copied, validated static instruction segments above the dynamic-context boundary while keeping Memory Recall and Compression summaries request-only.
- `model.call.started.context` now includes Instruction Stack message counts, total bytes, fixed built-in kind byte totals, and validation invariants for instruction-byte consistency.
- `observe.LogSink` emits selected Instruction Stack aggregate byte diagnostics without logging prompt bodies, segment sources, Skill names, Tool schemas, Message IDs, or Memory content.
- `agenttest.ValidateInstructionStackBudget` provides a content-free test guard for static instruction byte budgets using Context Manifests, and the compiled agenttest example demonstrates inspecting and budgeting Instruction Stack bytes.
- `agenttest.ValidateContextBudget` provides a broader content-free request-shape guard for Tool schema bytes, Tool arguments, text bytes, request/projected message counts, Memory projections, compression summaries, Tool Result blocks, Artifact blocks, and nested Instruction Stack budgets.
- The compiled agenttest example now demonstrates using `ValidateContextBudget` against a projected request with static instructions, a recalled Memory projection, and bounded Tool definitions.
- Documentation now covers Context Assembly, Context Governance, progressive capability disclosure, Context diagnostics, Instruction Stack budget inspection, the static v0.1.2 disclosure plan, and a v0.2.0 Dynamic Capability Activation draft.
- Documentation now clarifies the `StatusSuspended` / `Resume` state machine and provides an Enterprise persistence reference for database-backed run state, Worker leases, approval records, crash boundaries, and external-effect reconciliation while keeping those concerns outside Core.
- Built-in `read` now returns a content digest in both the human summary and `Result.State`, and built-in `edit` can reject stale `expected_digest` values or run as a non-writing `dry_run` while emitting before/after digests in its summary and state.
- Built-in `todo` now provides a bounded run-local structured checklist with round-trippable state for `list`, `add`, `update`, `complete`, `remove`, and `clear` actions, plus `ExtractLatestTodoState` for recovering the latest successful snapshot from Agent history.

### Compatibility

- `MetricKind` gains the additive `gauge` value, and the OTel adapter creates synchronous Float64 Gauges for the new AsyncSink runtime measurements.
- `ModelContextPayload` gains additive optional JSON fields for Instruction Stack diagnostics. Old Event Protocol V1 recordings without those fields remain valid.
- Existing callers that omit `agent.Config.ToolGroups` keep the compatibility default of exposing all registered Tools. Progressive capability disclosure remains opt-in for this change set.
- Custom instruction segment kinds may still render into the Instruction Stack, but Core exposes only fixed built-in kind byte fields in public Events and logs to avoid protocol and cardinality growth.
- Bounded catalog-summary helpers are deferred to the future dynamic activation protocol rather than added as a v0.1.2 API.
- Core still does not provide dynamic Tool activation during a running task, durable cross-process execution, database-backed State, or exactly-once external side effects.

## [0.1.1] - 2026-08-05

### Added

- Per-model-call stream budgets bound both the number of accepted Deltas and their cumulative UTF-8 string payload bytes before they enter Event Sinks or `Result.Events`.
- OpenAI-compatible Provider verification now combines deterministic HTTP/SSE reliability fixtures with an opt-in live call that can safely reuse a TMA dotenv without importing TMA packages, starting infrastructure, logging credentials, or enabling external network calls in normal CI.
- The optional standard-library `provider/openai` Adapter implements streaming text, Tool Call/Result mapping, Usage, request IDs, typed Provider failures, and caller-owned HTTP configuration without making Core depend on a vendor SDK.
- A deterministic Runtime reliability baseline now evaluates terminal and State invariants, stable error classification, diagnostic evidence, and undeclared delegate replay across 13 process-local fault scenarios, with versioned JSON and Markdown reports.
- The OpenAI-compatible Adapter now propagates bounded Provider request IDs on failures and classifies allowlisted quota, context-length, safety, authentication, permission, rate-limit, and model-availability error metadata without retaining Provider error messages.
- An explicit `compress_retry` Model Failure action can recover once per Model Target from a Provider context-length rejection by structurally compressing the failed request view, replanning it, and consuming a bounded additional model call.
- The opt-in `evaluation/compression` library runs application-owned Planners and Compressors against deterministic fact-source fixtures, structural/provenance checks, summary bounds, and post-compression budgets without retaining prompt or summary bodies in reports.
- Every new `model.call.started` payload includes a fixed-size, content-free Context Manifest with final Request composition counts and byte totals for Call-correlated diagnostics.

### Compatibility

- Existing explicit `agent.Limits` values that predate the stream-budget fields remain valid: zero `MaxModelDeltasPerCall` and `MaxModelDeltaBytesPerCall` values receive conservative defaults, while negative values are rejected.
- `CompressionRequest` and `context.compression.*` payloads now identify `budget_overflow` or `provider_context_length` as their Trigger; the latter is additive and disabled unless explicitly selected by Failure Policy. A missing Trigger remains compatible and is interpreted as the legacy `budget_overflow` path.
- `ModelCallPayload.Context` is an additive optional field. New Runtime calls populate it; Event Protocol V1 recordings produced before this change remain valid without it.

## [0.1.0] - 2026-08-04

### Added

- Process-local Tool approval policy with whole-batch zero-effect suspension, serializable digest-bound continuations, exact Resolution validation, `Agent.Resume`, approval lifecycle Events, and deterministic resume/rejection/tamper tests.
- Provider-neutral Message, Model, Usage, Provider Error, and Event Protocol V1 contracts.
- Bounded sequential Agent runtime with complete State validation and one terminal event.
- Native Tool and Toolkit contracts with immutable per-run registry snapshots.
- Structured Tool, Model, cancellation, limit, Event Sink, and internal failures.
- Bounded Model Retry/Fallback policy with `retry.scheduled` and `fallback.activated` events.
- Deterministic `agenttest` models, tools, clocks, IDs, and trace assertions.
- Draft 2020-12 Tool input validation with offline schema compilation.
- Tool namespaces, capability groups, Agent group projection, and bounded concurrency-safe Toolkit batches.
- Root-confined SafeSpace with bounded reads, atomic replacement writes, deterministic listing, and explicit non-durability semantics.
- Process-local Permission Policy with typed actions, default denial, immutable action allowlists, cancellation propagation, and panic containment.
- Permission-aware `builtin.read`, `write`, `edit`, `glob`, and `grep` tools with canonical paths, bounded text I/O/search, UTF-8 enforcement, and structured failures.
- Doublestar pattern matching through an ADR-approved dependency while Tiggy retains bounded FileSystem traversal.
- Injectable `command.Runner` contract, explicit standard-library `command/local` process adapter, and permission-aware bounded `builtin.bash` Tool.
- Bounded MCP Tool client with explicit lifecycle, native Tool projection, official SDK-backed stdio and Streamable HTTP transports, health checks, and non-replaying reconnect.
- Opt-in black-box stdio compatibility suite against pinned official TypeScript Everything and Filesystem MCP servers.
- Strict versioned Skill manifests, fail-closed SafeSpace discovery, bounded explicit Resource activation, immutable content digests, Agent Tool-Group projection, and `run.started` provenance.
- Validated token budget Planner with explicit count source, output/safety reservation, context pressure, per-Fallback planning, and paired context preparation events.
- Structurally safe Context Compressor contract with turn and Tool Call/Result grouping, protected active work, immutable full State, post-compression replanning, provenance, and fail-closed fallback.
- Versioned large Tool Result Artifact envelope, bounded Offloader and Store Ports, opaque SHA-256-bound references, provider-visible Artifact Content, paired offload events, and fail-closed Agent integration.
- Scoped bounded Memory Recall/Observe Port, request-only evidence projection, current-turn observation, configurable degradation, panic/cancellation containment, and paired non-sensitive lifecycle events.
- Explicit Noop and bounded concurrency-safe InMemory Memory Ports with copied seeds, newest-first scoped Recall, injected Admission, atomic commits, and no silent overwrite or eviction.
- Memory expiry enforced at both Agent and InMemory boundaries, Run/Message provenance validation for admitted Items, deep-copy ownership, expired-count diagnostics, and explicit reject/keep/replace exact-ID conflict policies.
- Read-only observation package with owned-event fanout, explicit strict/best-effort failure semantics, privacy-safe structured `slog`, concurrency-safe bounded-cardinality metrics, and official OpenTelemetry trace/metric export through application-owned Providers.
- Versioned Run Recorder with per-Event SHA-256 chains, partial/complete structural validation, default Model Delta and unknown-payload redaction, bounded concurrent recording, process-local Memory Journal, and caller-owned JSONL Journal/Reader.
- Deterministic `agenttest.FaultInjector` with exact-call Context Planner/Compressor/Token Counter and Memory Recall/Observe faults, panic/error/invalid-result/after-delegate actions, cancellation Watch synchronization, owned boundary values, and no implicit retry.
- Separate release-readiness documentation and a non-destructive repository audit for history, worktree, module, replacement, license, security-policy, changelog, and optional tag checks.
- A normal-CI fail-closed license gate for the complete reviewed Go module graph and a vulnerability scan using the pinned `govulncheck` command; external MCP compatibility remains scheduled and opt-in.
- An `agenttest` integration guide backed by a compiled Model-Tool-Model example covering full projected-context inspection, request assertions, deterministic IDs/time, call counts, final output, and trace invariants.
- A bilingual Getting Started guide that connects the offline example, Provider Adapter contract, Toolkit, RunStream, Memory, MCP, Skills, context planning, recording, and integration tests without implying bundled infrastructure.

### Changed

- Run terminal semantics now include `run.suspended`; completed, failed, and canceled States remain Tool-complete while a suspended State retains exactly one validated pending Tool batch.

- The minimum Go toolchain is 1.25.12 because earlier Go 1.25 patch releases contain reachable `net/url` and `os.Root` vulnerabilities.
- Toolkit now preserves a Tool-supplied `tool.RuntimeError` and its stable code.
- Agent error classification gives `tool.RuntimeError` precedence over nested model-actionable errors.
- SafeSpace now exposes canonical relative-path normalization for permission-aware FileSystem tools.
- SafeSpace now exposes a read-only `Reader` sub-interface for bounded resource discovery.
- `ModelTarget` and Agent Config can bind model-specific Context Planners; overflowing contexts now fail before provider invocation when planning is enabled.
- `ModelTarget` and Agent Config can bind model-specific Context Compressors; successful compression changes only the provider request view and never rewrites full Agent State.
- Agent Config can bind an Artifact Offloader. Successful large-result offload replaces the inline Tool Result with a validated reference; storage failures are run-fatal and are never retried automatically.
- Agent Config can bind a Memory Port, application-defined Scope, and bounded failure options. Recalled values affect only the current Run request view and are never appended to Agent State.
- SafeSpace List now reports a stable `not_directory` error for file targets.
- Indirect `golang.org/x/text` and `golang.org/x/sys` dependencies are pinned above versions affected by current vulnerability advisories.

### Compatibility

- This is the initial pre-release contract. No backward-compatible release baseline exists yet.
- Pre-v1 public APIs and protocols may change when documented in this changelog.
- Go 1.25.12 or later is required.

### Known Limitations

- `Run`, `Continue`, and `Resume` are process-local invocations; Core does not provide database checkpoints, Worker leases, or exactly-once external Tool effects.
- `command/local` and MCP stdio launch host processes and are not security sandboxes.
- The bundled Memory and Recorder implementations are process-local references; durable storage remains application-owned.
