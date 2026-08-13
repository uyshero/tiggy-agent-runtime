# Architecture and Boundaries

## Product Definition

Tiggy Agent Runtime is an in-process Go library. The caller owns construction, lifecycle, cancellation, configuration, and event consumption. The runtime may call remote model providers or MCP servers, but it does not call Tiggy Manage Agent through OpenAPI.

The target architecture is:

```text
Application
  -> Agent API
     -> Runtime supervisor
        -> Context manager
        -> Model
        -> Toolkit
           -> native tools
           -> MCP tools
           -> Skills
           -> built-in tools
        -> Memory manager
        -> Event stream
           -> observers
           -> run recorder
```

## Minimal Agent Loop

Strictly speaking, a minimal Agent Run is not "send a prompt to a model"; it is a bounded, verifiable, extensible execution loop:

| Component | Runtime responsibility |
| --- | --- |
| Goal and instructions | User input, `agent.Config.SystemPrompt`, `agent.Config.Instructions`, Skill activation, and the Instruction Stack define the task, durable rules, and dynamic-context boundary. |
| Model | The `model.Model` Port and optional `ModelTarget`/Fallbacks generate text or Tool Calls. Provider adapters perform calls; they do not own Agent policy. |
| Tool definitions | `tool.Tool`, `toolkit.Toolkit`, MCP Tools, Skill Tool Groups, and built-ins form an immutable per-Run capability snapshot. |
| Runtime controller | `agent.Agent` owns the bounded ReAct loop: Context Assembly, model calls, Tool Batch execution, Retry/Fallback, approval suspension, cancellation propagation, and Event emission. |
| Task state | `agent.State` stores continuable messages, usage, and optional approval Suspension. Memory Recall, Compression, and Artifact Offload affect projections or references instead of pretending to be ordinary history. |
| Result validation | Message/Tool Call/Tool Result, Tool Schema, Model Response, Compression Decision, Memory Result, Artifact Reference, and Event Sink outputs are validated and copied at boundaries. |
| Termination condition | Final response, failure, cancellation, approval suspension, or limit exhaustion. Each invocation must emit exactly one terminal Event. |

Without one of these pieces, a system may still call a model or execute a tool, but it is not yet a reliable Agent Runtime: without State it cannot Continue/Resume, without validation it can corrupt State, without termination it cannot prove a Run ended, and without a Tool snapshot it cannot explain which capabilities were model-visible.

## Dependency Direction

```text
agent
  -> message, model, event, tool, toolkit, skill, contextmanager, artifact, memory

orchestrator
  -> agent, message, Go standard library, approved JSON Schema validator

observe
  -> event, model, Go standard library

observe/otel
  -> observe, event, official OpenTelemetry Go API

record
  -> event, model, Go standard library

provider/openai
  -> message, model, net/http, Go standard library

toolkit
  -> message, model, tool

skill
  -> safespace, Go standard library only

contextmanager
  -> message, model, Go standard library only

artifact
  -> message, tool, Go standard library only

memory
  -> message, Go standard library only

mcp
  -> message, model, tool, official MCP Go SDK

builtin
  -> command, message, model, tool, safespace, permission

command
  -> Go standard library only

command/local
  -> command, safespace, Go standard library os/exec

safespace
  -> Go standard library only

permission
  -> Go standard library only

approval
  -> Go standard library only

model, message, event
  -> Go standard library only

tiggy-manage-agent
  -> tiggy-agent-runtime
```

There is no reverse dependency from this repository to `tiggy-manage-agent`.

## Core Responsibilities

- provider-neutral message and event contracts;
- reliable ReAct execution;
- bounded model and tool calls;
- cancellation and resource cleanup;
- Toolkit, MCP, Skills and built-in tools;
- root-confined SafeSpace and the injectable FileSystem interface;
- an injectable command-execution Port and explicitly selected adapters;
- context assembly, token planning, and semantics-preserving compression;
- large Tool Result Artifact offload through an injected Store Port;
- memory recall, observe and quality policies;
- structured errors, retries and fallback decisions;
- process-local Tool approval suspension and exact-call resume;
- optional synchronous fail-closed external Transition Hook and stable Tool idempotency keys;
- bounded sequential Brief orchestration, verified Artifact handoff, and
  injectable Artifact/Checkpoint storage Ports;
- logging, metrics, traces and run recording;
- deterministic testing and fault injection.

## Responsibilities Kept Outside

- HTTP, SSE and OpenAPI service endpoints;
- users, tenants, workspaces and RBAC records;
- PostgreSQL schemas and migrations;
- Worker queues, leases and distributed scheduling;
- Worker registration, polling, heartbeat, fencing tokens, retry scheduling,
  and orphan reaping;
- database-level durable Agent execution, lease/fencing, and recovery scheduling;
- enterprise approval identity, persistence, coordination and audit exports;
- billing, pricing catalogs and settlement;
- generated OpenAPI client SDKs.

## Runtime Guarantees

The runtime will be designed around these invariants:

1. A run emits exactly one terminal event.
2. Every accepted tool call reaches one terminal result, explicit interruption, or a validated approval suspension.
3. A tool result references an existing tool call and cannot be terminal twice.
4. Cancellation propagates to model, MCP, tool, memory, and FileSystem calls.
5. Iteration, model-call, tool-call, token, streamed Delta count/bytes, output, and deadline limits are bounded.
6. Concurrent tool events may interleave, but results are projected in model-call order.
7. Context compression preserves tool-call/result relationships and active work.
8. Extension failures are classified and cannot silently corrupt runtime state.
9. Every goroutine and owned resource has an explicit shutdown path.
10. Event sequence and parent correlation are stable and testable.

These are primarily in-process guarantees. The optional Transition Hook plus `Agent.Recover` can validate and apply a durably saved completed result across a process boundary. Core still does not select the latest boundary, coordinate recovery Workers, replay indeterminate effects, or guarantee exactly-once external side effects.

The `orchestrator` package adds a serializable `ExecutionSnapshot` and
compare-and-swap `CheckpointStore`, but it does not change that ownership
boundary. An Enterprise Worker must acquire and renew its lease before calling
`Orchestrator.Run` or `Resume`. Its adapter binds authorization and the current
fencing token to Artifact and checkpoint writes. Lease identity, tenant scope,
database transactions, and queue messages never enter Core values.

P1 currently uses sequential tool execution and a synchronous Event Sink. `Result.Events` is the complete internal diagnostic trace. Sink failure before a terminal event aborts the run; failure to deliver the terminal event preserves the actual execution status and returns a separate delivery error. See [Agent Runtime semantics](./runtime.md).

`observe` supplies read-only Sink composition, structured `slog` metadata, and bounded-cardinality metric derivation. `observe/otel` maps the same Events to application-owned OpenTelemetry Providers. Strict Sinks retain delivery-failure semantics; `BestEffort` explicitly contains operational observer errors and panics when monitoring must not affect execution. Neither package receives Agent State, owns background exporters, or logs raw Event payloads. See [Observation semantics](./observe.md).

`record` incrementally converts the same Event stream into a versioned, privacy-aware, per-Run SHA-256 chain. It validates generic sequence/parent/terminal invariants and supports bounded Memory and caller-owned JSONL Journals. Partial journals are diagnostic evidence, not resumable execution State; the digest is corruption detection rather than a signature. See [Run Recorder semantics](./record.md).

`agenttest.FaultInjector` wraps Context Planner/Compressor/Token Counter and Memory Recall/Observe boundaries with exact-call deterministic failures. It is test-only, owns no delegate resources, starts no timers/goroutines, and verifies error, panic, cancellation, invalid-result, and ambiguous-after-delegate paths without adding production fault controls. See [Context and Memory fault injection](./fault-injection.md).

## Extension Model

Extensions are separated by authority:

```text
Port        Replace an external capability: Model, Tool, Memory, FileSystem, Command Runner
Policy      Make a bounded decision: Retry, Compression, Permission, Scheduling
Middleware  Wrap an operation: tracing, logging, caching
Observer    Consume events without changing runtime decisions
```

Observers are read-only. Middleware must not receive unrestricted mutable runtime state. Policies return typed decisions. Ports own only the resources they create.

`command.Runner` is the Core Port used by `builtin.bash`. Core does not select an execution backend. The optional `command/local` adapter uses `os/exec`; its Root constrains only the initial working directory and is not a filesystem, network, or process sandbox. Applications that need isolation must inject a container, VM, remote executor, or another purpose-built Runner.

`mcp.Client` owns an explicitly connected Session and projects discovered remote Tools into the native Tool contract. Official SDK types remain private. Reconnect replaces the Session but never replays a Tool Call because transport failure leaves external effects ambiguous.

`skill.Catalog` discovers strict versioned manifests through a caller-owned read-only SafeSpace contract. Applications explicitly activate trusted instructions, selected Resources, and declared Toolkit Groups before Agent construction. Skills do not execute code, fetch packages, or acquire authority dynamically.

## Context and Memory

The architecture distinguishes:

```text
Context   What the next model request sees
Memory    Information retained across runs or long conversations
Artifact  Large outputs stored outside model context
```

Context Assembly first renders the application SystemPrompt and explicitly activated Skills into an Instruction Stack with a dynamic-context boundary. Context preparation accounts for the system prompt, tool schemas, output reserve, active tool relationships, recent messages, recalled memory, and model window. Compression operates on semantic groups rather than arbitrary character ranges.

`contextmanager.TokenPlanner` performs immutable preflight accounting through an injected provider-aware or explicitly estimated Counter. On overflow, an optional model-specific `Compressor` receives only Core-built inactive semantic groups. Core preserves System messages, the latest turn, unresolved Tool Calls, and Tool Call/Result relationships; constructs the summary replacement itself; and replans once before provider invocation. An explicit Failure Policy may use the same boundary once when a Provider reports Context Length after a fitting preflight, covering tokenizer or endpoint drift without inventing an overflow value. Compression changes only the request view, not full Agent State. Failure or insufficient reduction falls back to fail-closed rather than lossy deletion.

These are structural guarantees. Core records strategy/version and source Message IDs, but it cannot prove that an injected summary is factually faithful. Compressor quality and any external resources it owns remain application responsibilities. The opt-in `evaluation/compression` package helps applications enforce source-bound fact, size, provenance, and post-compression budget fixtures without becoming a Runtime dependency. See [ADR 0007](./adr/0007-context-compression.md) and [ADR 0016](./adr/0016-compressor-quality-evaluation.md).

Each new physical Model Call also records a fixed-size, content-free final Context Manifest on its start Event. It correlates request-shape diagnostics with the Call ID without giving observers Agent State or request bodies. See [Context diagnostics](./context-diagnostics.md) and [ADR 0017](./adr/0017-model-context-diagnostic-manifest.md).

Large successful Tool Results may pass through an optional `artifact.Offloader` before State and model projection. Core encodes the complete result in a versioned envelope, binds it to SHA-256, and accepts only opaque references matching its media type, size, and digest. The injected Store owns durability, authorization, encryption, retention, and garbage collection. Core never places signed URLs in Message/Event values, never retries a Put, and never auto-fetches an Artifact into context. See [Artifact offload semantics](./artifact.md).

The injected `memory.Port` performs one scoped Recall before model execution and one Observe after successful response commit. Recall changes only the request view, while Observe receives only the current turn. Ordinary failures degrade by default; cancellation and invalid/cross-scope results remain fatal. Core provides bounds and events but no durable storage implementation. See [Memory Recall and Observe semantics](./memory.md).

Core includes Noop and bounded process-local InMemory reference Ports. InMemory requires explicit Admission before Observe stores Items and makes no durability claim; production storage remains an injected Adapter responsibility. Exact Scope, inclusive expiry, source correlation, and explicit ID-conflict behavior are structural rules. Agent rechecks expiry before model projection, while semantic truth and physical deletion remain outside Core.

Confidence, semantic deduplication/merge, contradiction handling, physical deletion, and fact-admission decisions remain explicit application policies rather than storage-specific hidden behavior.

## Repository Strategy

This repository is physically independent from `tiggy-manage-agent`. It owns its module, CI, release notes, compatibility policy, and public API. During local migration, Enterprise may use a temporary Go `replace` directive. Published releases must use tagged module versions.
