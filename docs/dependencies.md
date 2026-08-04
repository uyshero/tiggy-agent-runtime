# Dependency Policy

## Current Dependency Baseline

The `v0.0.x` protocol packages use only the Go standard library. Toolkit has an ADR-approved JSON Schema dependency, built-in search has an ADR-approved doublestar matcher, MCP transport compatibility uses the ADR-approved official Go SDK, and the optional observation Adapter uses ADR-approved official OpenTelemetry modules. Dependencies are not added early for roadmap-only capabilities.

Dependencies are added only for implemented capabilities. A roadmap item is not sufficient reason to add a module.

## Layer Rules

### Foundation packages

The following packages must remain standard-library-only unless an accepted ADR proves otherwise:

```text
message
model
event
permission
command
agent state and runtime contracts
```

These packages define the public protocol and must not expose third-party types in exported signatures.

### Runtime packages

Agent, Toolkit, built-in Tool, context, artifact, memory, and observation packages may depend on small, audited libraries when the dependency removes substantial protocol or concurrency risk. They must still expose Tiggy-owned interfaces and values.

### Adapter packages

Provider, MCP, OpenTelemetry, vector-store, remote FileSystem, and isolated Command Runner adapters may depend on their official or established client libraries. Adapter dependencies must not leak into foundation packages. The `mcp` package exposes only Tiggy and standard-library types even though its private transport adapter imports the official SDK.

`command/local` is the standard-library local-process adapter. It depends only on `command`, `safespace`, and `os/exec`; it adds no module dependency. Importing it is an explicit application choice, not a transitive requirement of `builtin.bash`.

`skill` uses strict standard-library JSON parsing and the standard-library-only `safespace.Reader` contract. Skill discovery and activation add no Go module, YAML parser, package manager, or Node.js dependency. See [ADR 0005](./adr/0005-strict-skill-manifest.md).

`contextmanager` uses the existing `model.TokenCounter` Port and standard-library arithmetic only. Token planning adds no tokenizer dependency because provider-specific tokenization remains in injected adapters. Semantic grouping and summary replacement also use only Tiggy protocol values and the standard library; a Compressor implementation remains caller-injected. See [ADR 0006](./adr/0006-token-budget-planner.md) and [ADR 0007](./adr/0007-context-compression.md).

`artifact` uses SHA-256, JSON, Tiggy Message/Tool values, and the standard library only. Core includes no object-store SDK; applications inject an `artifact.Store` Adapter with their own durability, authorization, encryption, retention, and cleanup semantics. See [ADR 0008](./adr/0008-tool-result-artifact-offload.md).

`memory` uses Tiggy Message values, `sync`, and the standard library only. Its Noop and InMemory reference Adapters add no module or persistence dependency. Core includes no database, vector client, tokenizer, or embedding dependency; applications inject an Enterprise `memory.Port` Adapter and keep backend identity/record types private. See [ADR 0009](./adr/0009-memory-recall-observe.md) and [ADR 0010](./adr/0010-process-local-memory-adapters.md).

`observe` uses Event/Model values and the standard library only. `observe/otel` pins the official OpenTelemetry Go API and SDK at v1.44.0; SDK packages verify export behavior in tests. OTel types remain isolated to that optional Adapter, and applications own Providers, Exporters, background resources, and shutdown. See [ADR 0012](./adr/0012-observation-adapters.md).

`record` uses Event/Model values, JSON, SHA-256, bounded standard-library I/O, and synchronization only. Memory/JSONL Journals add no database, object-store, logging, or replay dependency. See [ADR 0013](./adr/0013-run-recorder.md).

`agenttest` fault injection uses only existing Tiggy Context/Memory/Model contracts, Context cancellation, and standard-library synchronization. It adds no chaos, timing, random, proxy, or infrastructure dependency. See [ADR 0014](./adr/0014-deterministic-fault-injection.md).

## Forbidden Dependencies

This repository must not import or require:

- `tiggy-manage-agent` packages;
- PostgreSQL clients or `database/sql` in Core packages;
- TMA HTTP handlers, Worker runtime, tenant types, or database records;
- generated TMA OpenAPI clients;
- browser, UI, or Node.js build dependencies;
- packages that require CGO unless approved through an ADR and isolated in an optional adapter.

## Dependency Admission Checklist

Every new direct dependency requires a PR note covering:

1. The implemented capability that needs it.
2. Why the standard library or a small local implementation is insufficient.
3. License compatibility and source ownership.
4. Maintenance activity and release history.
5. Known security advisories.
6. Transitive dependency count and binary-size impact.
7. Whether it starts goroutines or owns network/file resources.
8. Cancellation, timeout, and shutdown behavior.
9. Whether its types appear in public API.
10. Replacement or removal strategy.

## Planned Dependency Decisions

No library is preselected merely by category. Before implementation, create an ADR for:

| Capability | Decision required |
|---|---|
| JSON Schema | decided by [ADR 0002](./adr/0002-json-schema-validation.md) |
| Glob matching | decided by [ADR 0003](./adr/0003-doublestar-glob-matching.md) |
| MCP transport | decided by [ADR 0004](./adr/0004-official-mcp-go-sdk.md); pinned external stdio matrix is documented in [MCP Client semantics](./mcp.md#external-compatibility) |
| OpenTelemetry | decided by [ADR 0012](./adr/0012-observation-adapters.md); optional Adapter, application-owned Providers, and bounded semantic conventions |
| Tokenization | provider accuracy, fallback estimation, model coverage |
| Vector memory | Port decided by [ADR 0009](./adr/0009-memory-recall-observe.md); backend client, embeddings, indexing, and quality policy remain Adapter decisions |
| Isolated command execution | `command.Runner` contract before Docker/Kubernetes/remote clients |

## Versioning and Reproducibility

- Commit `go.mod` and `go.sum` together.
- Pin released module versions; do not depend on branches in release commits.
- Run `go mod tidy` after dependency changes.
- CI builds with the Go version declared in `go.mod`.
- Public releases must be reproducible without neighboring repositories.
- Temporary local `replace` directives belong in a developer `go.work`, not in release `go.mod`.

## Security Automation

Before the first public release, CI must add vulnerability and license checks. A failing vulnerability check may be waived only with a documented assessment, owner, expiry date, and compensating control.
