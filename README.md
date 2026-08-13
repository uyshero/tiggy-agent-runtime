# Tiggy Agent Runtime

[English](./README.md) | [简体中文](./README.zh-CN.md)

Tiggy Agent Runtime is an embeddable Go runtime for reliable tool-using agents. It focuses on execution correctness, structured failures, context and memory governance, lifecycle observability, controlled extensibility, and optional fail-closed durable transition checkpoints.

This repository is a runtime library, not an OpenAPI client SDK:

```text
Go application -> import this module -> run the agent in the same process
```

The generated clients that call Tiggy Manage Agent over HTTP/SSE remain separate Enterprise deliverables.

## Status

P3 implementation is complete. The repository now provides provider-neutral contracts, an optional OpenAI-compatible Adapter, a bounded sequential ReAct loop, digest-bound Tool approval suspension and resume, an optional fail-closed Durable Transition Hook with validated completed-result recovery, Toolkit and built-in execution boundaries, MCP transports with an external compatibility suite, explicit Skill activation, token budget planning, structurally safe context compression, bounded large Tool Result offload, scoped Memory quality contracts, read-only logging/metrics/OpenTelemetry observers, a versioned integrity-linked Run Recorder, and deterministic fault evaluation. The current version is not yet production-ready.

## Module

```text
github.com/uyshero/tiggy-agent-runtime
```

Current packages:

```text
message/  Model-visible messages, content blocks, tool calls and results
model/    Model generation, streaming, usage and provider-error contracts
event/    Versioned runtime lifecycle-event envelope
tool/     Native Tool, Result, model-actionable errors and runtime errors
toolkit/  Concurrent registry and immutable per-run snapshots
agent/    Bounded Agent loop, State, structured RunError and events
orchestrator/  Sequential orchestration, verified artifacts, checkpoints, and Resume
examples/orchestrator/ Isolated two-task Artifact handoff example
agenttest/ Deterministic models, tools, clocks, IDs, fault injection, and trace validation
safespace/ Root-confined bounded local file operations and deterministic listing
permission/ Process-local Tool effect decisions and action allowlists
approval/ Whole-batch Tool approval decisions and serializable continuations
builtin/  Bounded permission-aware tools supplied by the runtime
command/  Bounded command contract and the opt-in local process adapter
mcp/      Bounded MCP Tool discovery, calls, lifecycle, stdio, and HTTP
skill/    Strict versioned Skill discovery and explicit immutable activation
contextassembly/ Static instruction stack and dynamic-context boundary rendering
contextmanager/ Token planning, semantic compression groups and validated summaries
artifact/ Versioned large Tool Result offload and opaque object references
memory/   Scoped Recall/Observe contracts and bounded process-local adapters
observe/  Structured logging, bounded metrics, and optional OpenTelemetry adapters
record/   Privacy-aware integrity-linked Memory and JSONL Run journals
provider/openai/ Optional standard-library OpenAI-compatible Model adapter
evaluation/ Deterministic Runtime reliability and Compressor quality reports
```

## Run the Example

The example needs no model credentials:

```bash
go run ./examples/basic-agent
```

Applications replace its `demoModel` with a provider adapter implementing `model.Model` and inject it through `agent.Config`. The runtime does not read global provider credentials.

## Verification

Requires Go 1.25.12 or later:

```bash
make verify
```

Verification includes formatting, dependency-boundary and license checks, `go vet`, unit tests, race detection, and the deterministic Runtime reliability baseline. Run that baseline alone with `make eval-reliability`.

Release metadata is audited separately because it includes owner-controlled decisions that must not block normal development:

```bash
make release-audit
make release-verify RELEASE_VERSION=v0.2.3
```

See [Release readiness](./docs/release-readiness.md) for the current technical gates, blockers, and owner decisions. Passing the audit is necessary but does not authorize publication.

## Design Documents

- [Getting started](./docs/getting-started.md)
- [Architecture and boundaries](./docs/architecture.md)
- [Dependency policy](./docs/dependencies.md)
- [Engineering standards](./docs/standards.md)
- [Agent Runtime semantics](./docs/runtime.md)
- [Model contract notes](./docs/model-contract.md)
- [Toolkit semantics](./docs/toolkit.md)
- [SafeSpace semantics](./docs/safespace.md)
- [Permission Policy semantics](./docs/permission.md)
- [Tool approval and resume semantics](./docs/approval.md)
- [Durable Transition Hook](./docs/transition.md)
- [Enterprise Runtime persistence reference](./docs/enterprise-runtime.md)
- [Orchestrator Enterprise integration](./docs/orchestrator-enterprise.md)
- [Command Runner semantics](./docs/command.md)
- [MCP Client semantics](./docs/mcp.md)
- [Skill semantics](./docs/skill.md)
- [Context Assembly and Instruction Stack](./docs/context-assembly.md)
- [Context Governance](./docs/context-governance.md)
- [Context Manager semantics](./docs/contextmanager.md)
- [Context diagnostics](./docs/context-diagnostics.md)
- [Dynamic Capability Activation draft](./docs/dynamic-capability-activation.md)
- [Artifact offload semantics](./docs/artifact.md)
- [Memory Recall and Observe semantics](./docs/memory.md)
- [Observation semantics](./docs/observe.md)
- [Run Recorder semantics](./docs/record.md)
- [Context and Memory fault injection](./docs/fault-injection.md)
- [Agent test kit guide](./docs/agenttest.md)
- [Fuzzing and benchmarks](./docs/fuzz-and-benchmarks.md)
- [Live Provider contract verification](./docs/live-provider.md)
- [OpenAI-compatible Provider Adapter](./docs/provider-openai.md)
- [Runtime reliability evaluation](./docs/reliability-evaluation.md)
- [Compressor quality evaluation](./docs/compression-evaluation.md)
- [Progressive capability disclosure plan](./docs/progressive-disclosure.md)
- [Built-in Tool semantics](./docs/builtin.md)
- [Extraction and migration plan](./docs/migration.md)
- [Roadmap](./docs/roadmap.md)
- [Release readiness](./docs/release-readiness.md)
- [ADR 0001: Bounded model failure recovery](./docs/adr/0001-model-failure-recovery.md)
- [ADR 0002: Tool argument JSON Schema validation](./docs/adr/0002-json-schema-validation.md)
- [ADR 0003: Doublestar Glob pattern matching](./docs/adr/0003-doublestar-glob-matching.md)
- [ADR 0004: Official MCP Go SDK](./docs/adr/0004-official-mcp-go-sdk.md)
- [ADR 0005: Strict Skill manifest and explicit activation](./docs/adr/0005-strict-skill-manifest.md)
- [ADR 0006: Explicit token budget planning](./docs/adr/0006-token-budget-planner.md)
- [ADR 0007: Structurally safe context compression](./docs/adr/0007-context-compression.md)
- [ADR 0008: Large Tool Result Artifact offload](./docs/adr/0008-tool-result-artifact-offload.md)
- [ADR 0009: Scoped Memory Recall and Observe](./docs/adr/0009-memory-recall-observe.md)
- [ADR 0010: Process-local Memory adapters](./docs/adr/0010-process-local-memory-adapters.md)
- [ADR 0011: Memory quality contract](./docs/adr/0011-memory-quality-contract.md)
- [ADR 0012: Read-only observation adapters](./docs/adr/0012-observation-adapters.md)
- [ADR 0013: Versioned integrity-linked Run Recorder](./docs/adr/0013-run-recorder.md)
- [ADR 0014: Deterministic Context/Memory fault injection](./docs/adr/0014-deterministic-fault-injection.md)
- [ADR 0015: Bounded Provider context-length recovery](./docs/adr/0015-provider-context-length-recovery.md)
- [ADR 0016: Deterministic Compressor quality evaluation](./docs/adr/0016-compressor-quality-evaluation.md)
- [ADR 0017: Content-free Model Context diagnostic manifest](./docs/adr/0017-model-context-diagnostic-manifest.md)
- [ADR 0018: Progressive capability disclosure](./docs/adr/0018-progressive-capability-disclosure.md)
- [ADR 0019: Dynamic capability activation](./docs/adr/0019-dynamic-capability-activation.md)
- [ADR 0020: Durable Transition Hook](./docs/adr/0020-durable-transition-hook.md)
- [ADR 0021: Recovery Continuation API](./docs/adr/0021-recovery-continuation-api.md)
- [Compatibility changelog](./CHANGELOG.md)

## Relationship to Tiggy Manage Agent

`tiggy-manage-agent` is the Enterprise service and may depend on this module. This module must never depend on the Enterprise repository, its database schema, Worker runtime, OpenAPI service contract, or tenant model.

The project is licensed under Apache-2.0. Publication remains an explicit owner-controlled release action; a prepared version section is not itself a published release.
