# Engineering Standards

## Public API

- Export the smallest coherent API; keep implementation details in `internal/`.
- Accept `context.Context` as the first parameter of every blocking or I/O operation.
- Do not store caller contexts in structs.
- Do not expose Enterprise IDs, database rows, OpenAPI DTOs, or third-party client types.
- Prefer explicit structs and typed decisions over `map[string]any`.
- Use `json.RawMessage` only at intentional protocol extension boundaries.
- Document ownership of slices, maps, byte buffers, streams, and returned resources.
- Exported mutable values crossing asynchronous boundaries must be copied or transferred explicitly.

## Errors

- Errors must preserve their cause with `%w` or `Unwrap`.
- Errors shown to a model or user must contain only explicitly safe detail.
- Model/provider failures use stable classes such as authentication, rate limit, context length, timeout, network, stream protocol, canceled, and unknown.
- Tool input and business failures become structured Tool Results when the model can act on them.
- Runtime corruption, invalid protocol transitions, and extension contract violations terminate the run.
- Retries require a typed retryable decision, a finite attempt limit, cancellation support, and observable scheduling.
- Authentication, permission, invalid request, and deterministic validation failures are not retried by default.

## Events and Observability

- Every run has a stable `run_id`; nested operations have IDs and parent correlation.
- Event sequence is positive and monotonic within one run.
- A run emits exactly one of completed, failed, or canceled.
- Start/end event pairs must be balanced, including error and cancellation paths.
- Critical events cannot be dropped: terminal events, runtime errors, final tool results, context-compression outcomes, and Artifact offload outcomes.
- Delta events may be coalesced under documented backpressure policy.
- Logs and traces do not include credentials, full prompts, or raw sensitive tool arguments by default.
- Metrics use bounded-cardinality labels; IDs never become metric labels.
- Operational observers use an explicit best-effort boundary when telemetry failure must not fail a Run; strict delivery remains available for audit and recording.
- Default logs, metrics, and traces exclude raw Model deltas, Tool arguments/results, Message/Memory content, Scope keys, and Artifact bodies.
- Run recordings use an explicit protocol version, bounded incremental writes, payload sanitization, contiguous sequences, terminal completeness checks, and per-Run integrity links.
- Recording digests are never described as signatures or durability; Journal ownership, flush, authentication, retention, and deletion remain explicit.

## Concurrency and Lifecycle

- Every goroutine must have a documented owner and termination condition.
- Every network client, stream, subprocess, and temporary file must have an owner and close path.
- Cancellation must propagate through Model, MCP, Tool, Memory, and FileSystem calls.
- Channels have documented producer ownership and close behavior.
- Concurrent tools declare their concurrency class; unsafe tools execute sequentially.
- Concurrent event production may interleave, but context projection preserves model-call order.
- CI runs `go test -race ./...`.

## Context Management

- Context preparation reserves output capacity before selecting history.
- Token accounting includes system instructions, tool schemas, recalled memory, active tool relationships, and recent messages.
- Token count source, context window, safety margin, utilization, and exact overflow are explicit Plan fields.
- A custom Plan is validated before provider invocation; overflow never silently falls through to a model call.
- Fallback models use model-specific Planners because windows and tokenizers may differ.
- Tool Call and Tool Result blocks form indivisible compression groups.
- Active or unresolved calls cannot be compressed away.
- Compression records source coverage, input/output token estimates, strategy identity, and fallback behavior.
- Compression failure has a bounded fallback and cannot silently delete context.
- A context-length provider error may trigger at most one policy-controlled compress-and-retry cycle by default.

## Memory Management

- Context and long-term memory are separate interfaces and lifecycles.
- Memory records carry scope and provenance before cross-run recall is enabled.
- Expiry is inclusive and enforced again by Agent before model projection; it does not imply physical deletion.
- Observe-admitted Items identify the current Run and at least one actual source Message.
- Recall and observe failures are observable and degradable by default.
- Cancellation, panic, crossed-scope recall, malformed items, and bound violations cannot be silently degraded.
- Recalled values enter only the model request view; Observe receives only the completed current turn.
- Scope isolation, inclusive expiry, deduplication, and exact-ID conflict handling must be tested; physical deletion remains a later or Enterprise adapter requirement.
- Tool failures and unverified model claims are not stored as facts without a policy decision.
- Memory adapters do not expose backend-specific record types in Core APIs.
- Core Memory contracts do not imply a database, vector index, or durability guarantee.
- Process-local Memory is bounded, admits nothing without an explicit policy, commits atomically, and never silently overwrites or evicts Items.
- Exact-ID conflict behavior is explicit; semantic merge, confidence, and contradiction resolution are never inferred by Core.

## Artifacts

- Large Tool Result offload happens before model projection and uses a versioned, integrity-bound payload.
- Object references are opaque identifiers, never signed URLs, credentials, or backend records.
- A reference records media type, byte size, and SHA-256 digest and must match the stored payload.
- Store durability, authorization, encryption, retention, retrieval, and garbage collection are explicit Adapter responsibilities.
- Put is never retried automatically because cancellation or transport failure can leave storage effects ambiguous.
- Offload failure is observable and fail-closed; Core cannot silently inline an over-boundary result.
- Core does not auto-fetch Artifact content. Model access requires an explicitly authorized retrieval Tool.

## Toolkit, MCP, and Skills

- Native, MCP, Skill, and built-in tools share one Tool Call and Tool Result protocol.
- Tool names are unique after namespacing and validated before model invocation.
- Input Schema is valid JSON and validated before execution.
- Tool errors distinguish model-correctable failures from runtime failures.
- Tool output, duration, and concurrency are bounded.
- MCP clients have explicit connect, health, reconnect, cancellation, and close semantics.
- MCP discovery and wire messages are bounded before definitions or results enter model context.
- MCP transport failures never trigger implicit Tool-call replay; external effects are ambiguous until the application resolves them.
- MCP SDK types remain private to the adapter and cannot appear in Core public signatures.
- Skills are versioned resources with bounded instructions and explicit tool/resource activation.
- Skill discovery is fail-closed; strict manifests reject unknown/duplicate fields and path escape before publishing a Catalog.
- Skill and Resource selection is validated before content I/O; active content is immutable and carries a reproducible digest.
- Activated Skill content has System authority and must come from an application-authorized source.
- Core does not auto-download, execute, or model-select Skills; external formats require explicit adapters.
- Built-in filesystem tools cannot escape the SafeSpace root.
- Built-in tools check a typed Permission Action after input validation and before external effects.
- Built-in filesystem tools normalize the path before using the same Target for Permission and I/O.
- Recursive search has explicit entry, depth, result, output, and per-file limits.
- Shell execution requires an explicitly injected Command Runner; Core never installs a default local executor.
- Command scripts are excluded from Permission Requests and default logs; authorization uses only the canonical working directory.
- A local process Root must not be documented as sandboxing absolute paths, network access, descendants, or other host resources.
- Command Runners honor cancellation and bound scripts and captured output; cancellation does not promise rollback of external effects.
- Permission denial is model-actionable; invalid requests and Policy failures are runtime failures.

## Extension Safety

- Ports replace capabilities; Policies make decisions; Middleware wraps operations; Observers are read-only.
- Middleware does not receive unrestricted mutable Agent state.
- Extension ordering is deterministic and documented.
- Extension panic is recovered at the plugin boundary and converted to a structured runtime failure.
- Optional extensions declare whether failure is degradable or run-fatal.
- Extension timeouts and resource ownership are explicit.

## Testing

Every behavior change includes tests proportional to risk. Required categories are:

- protocol validation and cloning;
- happy-path model/tool execution;
- terminal-event uniqueness;
- cancellation at every external boundary;
- timeout and retry limits;
- malformed model/MCP/tool responses;
- context pressure and compression failure;
- memory failure and scope isolation;
- concurrent tool ordering and partial failure;
- slow event consumers and backpressure;
- panic containment and resource cleanup;
- race detection.

Deterministic fault rules target exact call ordinals. Cancellation tests synchronize on boundary entry rather than sleep. After-delegate faults assert side-effect count and no implicit replay, not only the returned error.

Tests must be deterministic by default and require no real model, database, or network. External compatibility tests are opt-in and separated from unit CI.

## Compatibility

- Public Go APIs follow semantic versioning after `v1.0.0`.
- Before `v1`, breaking changes are allowed but documented in release notes.
- Serialized Message, Event, and snapshot formats carry explicit protocol versions before persistence is supported.
- Adding an event type is additive; changing an existing event's meaning is breaking.
- Error classes and terminal reasons are compatibility contracts.
- Deprecations include a replacement and removal target.

## Change Requirements

A PR that changes a public contract must include:

- motivation and non-goals;
- compatibility impact;
- event and error impact;
- cancellation and resource ownership impact;
- tests, including failure paths;
- documentation update;
- an ADR when the decision is difficult to reverse.
