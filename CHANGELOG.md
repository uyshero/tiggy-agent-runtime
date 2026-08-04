# Changelog

All compatibility-relevant changes to public Go APIs, Message/Event protocols, error classes, and terminal reasons are recorded here before release.

## Unreleased

### Added

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
- A normal-CI vulnerability scan using the pinned `govulncheck` command; external MCP compatibility remains scheduled and opt-in.

### Changed

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
- Public distribution remains blocked until repository ownership and licensing are approved.
