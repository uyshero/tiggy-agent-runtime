# Extraction and Migration Plan

## Source Repository

The source implementation currently lives in `tiggy-manage-agent`. That worktree may contain unrelated in-progress changes and must not be modified or cleaned up as part of Runtime extraction.

Extraction is semantic, not a directory copy. Enterprise-only types and guarantees remain in the Enterprise repository.

## Classification

### Extract now

| Existing area | Runtime destination | Required change |
|---|---|---|
| `internal/model` message blocks | `message` | remove Enterprise identity assumptions; tighten union validation |
| `internal/model` model stream | `model` | replace Session/Turn/Attempt with Run/Call identity |
| `internal/model` provider errors | `model` | retain safe detail and error chaining |
| Agent lifecycle event vocabulary | `event` | use versioned envelope and correlation fields |

### Extract after interface redesign

| Existing area | Runtime destination | Reason |
|---|---|---|
| model provider adapters | `adapters/model/...` | currently tied to Enterprise routing and credentials |
| tool registry and executor | `toolkit`, `tool` | currently tied to policies, artifacts, and managed environments |
| MCP client/host | `mcp` | separate transport lifecycle from registry persistence and tenant policy |
| filesystem/process tools | `builtin`, `safespace` | replace Enterprise sandbox records with the FileSystem interface |
| context building and compaction | `contextmanager` | remove Session/Turn storage and expose token planning policy |
| tracing and metrics | `observe` | remove service-specific labels and transport assumptions |
| model/tool test fakes | `agenttest` | publish deterministic scenario and fault-injection API |

### Keep in Enterprise

| Existing area | Reason |
|---|---|
| `internal/agentcore` durable State/Phase | contains revision, Session, Turn, approval, completion, and durability semantics |
| `DurabilityPort` and PostgreSQL implementation | database-level execution guarantee |
| Worker queue, lease, attempt and fencing | distributed scheduling concern |
| approval/intervention records | enterprise workflow and audit concern |
| Completion Gate tied to Task Plan evidence | enterprise product policy |
| Workspace/Owner access scope and RLS | tenant isolation concern |
| OpenAPI SDKs under `sdk/` | remote Enterprise client, not embedded Runtime |

## Migration Stages

### Stage 0: repository baseline

- independent module, CI, standards, and dependency guard;
- Message, Model, Error, Usage, and Event contracts;
- no dependency on Enterprise.

### Stage 1: reliable local runtime

- Agent API and state;
- bounded ReAct loop;
- terminal-event and tool-result invariants;
- cancellation and resource cleanup;
- deterministic model and tool fakes.

### Stage 2: tools and extensions

- Toolkit and native tools;
- SafeSpace and safe built-ins;
- MCP lifecycle and compatibility suite;
- Skills and typed extension contracts;
- permission and tool scheduling policies.

### Stage 3: context, memory, and observation

- token budget planner, compression, and large Tool Result Artifact offload;
- memory recall/observe and quality policies;
- logs, metrics, OTel and Run Recorder;
- fault injection and long-running tests.

### Stage 4: Enterprise adoption

- Enterprise adds a tagged Runtime dependency;
- adapters translate Enterprise routing, tools, file access, and observation to Runtime interfaces;
- legacy and Runtime paths run contract-equivalence tests;
- migration uses a feature flag and explicit rollback window;
- old duplicate code is removed only after production verification.

## Local Development During Migration

Use a developer-owned `go.work` outside release commits:

```bash
go work init ./tiggy-agent-runtime ./tiggy-manage-agent
```

Do not commit a permanent relative `replace` directive to either released module. CI must verify each repository independently before cross-repository integration tests run.

## Exit Criteria

Enterprise adoption is complete only when:

- Enterprise imports no duplicated Runtime protocol types;
- adapters are the only Enterprise-to-Runtime translation layer;
- embedded Runtime tests require no Enterprise repository;
- Enterprise tests cover cancellation, event mapping, context, tool errors, and configuration identity;
- independent tagged releases and rollback instructions exist.
