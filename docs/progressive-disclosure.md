# Progressive Capability Disclosure Plan

## Problem

Tool Definitions are part of every provider Request. A large Toolkit can consume a meaningful part of the context window before the conversation begins, while exposing unrelated capabilities increases model selection errors and the blast radius of a mistaken Tool Call.

The Runtime already has the primitives for a conservative solution: Toolkit Groups, immutable per-Run snapshots, Skill discovery/activation, MCP catalog bounds, Permission Policy, Approval suspension, and the content-free Model Context Manifest. This plan composes those primitives without changing Tool execution semantics.

## Principle

Expose the smallest capability set needed for the current task, and make each expansion explicit and observable:

```text
bounded discovery -> explicit selection -> immutable snapshot -> execution policy -> Tool Call
```

Progressive disclosure controls context and choice. It does not grant permission, approve side effects, or prove that a Tool is safe. Permission and Approval remain mandatory independent boundaries.

## Exposure Levels

### Level 0: No Tools

Use for pure conversation, planning, or untrusted input triage. The Model Request contains no Tool Definitions.

### Level 1: Bounded Catalog

Expose only bounded metadata owned by the application: capability group, short summary, availability, and optional risk class. Do not include complete JSON Schemas, credentials, backend identifiers, or hidden Tool arguments. Catalog results have explicit item and byte limits.

### Level 2: Group Activation

The application selects one or more trusted Groups and creates a Toolkit Snapshot. The Model receives the complete Definitions only for that Snapshot. Input Schemas remain byte-for-byte identical to the definitions compiled by Toolkit.

### Level 3: Execution

The Runtime validates the Tool Call, checks Permission, and evaluates Approval Policy for the whole Tool Batch before execution. Disclosure never bypasses these checks.

## Existing Core Mapping

| Requirement | Existing Core boundary |
|---|---|
| Select a capability subset | `agent.Config.ToolGroups` and `Toolkit.SnapshotGroups` |
| Keep the set stable | immutable Toolkit View created for a Run |
| Bound discovered capabilities | MCP page/tool/catalog limits and Skill Catalog limits |
| Count schema cost | injected `model.TokenCounter` plus `model.call.started.context` aggregates |
| Prevent unauthorized effects | Permission Policy and Approval Policy |
| Explain what was used | `run.started` provenance, Tool lifecycle Events, and Context Manifest |

The initial implementation should use these APIs. Existing callers that do not configure Groups retain their current behavior; progressive exposure is opt-in until a compatibility review approves another default.

## Delivery Plan

### v0.1.2: Static Exposure Hardening

- Document `ToolGroups` as the supported progressive-disclosure mechanism.
- Add deterministic tests for zero, one-group, and multi-group projections, including schema byte budgets observed in `model.call.started.context`.
- Add an external consumer example that selects Groups before `Run` and asserts the exact Tool set in `agenttest.ModelStep.Assert`.
- Add release documentation and migration guidance. Do not change the default “all registered Tools” behavior in this patch release.
- Defer bounded catalog-summary helpers to the dynamic activation design, where catalog evidence, activation authorization, stale-catalog behavior, and source-specific limits can be specified together.

### v0.2.0 or Later: Explicit Dynamic Activation

Only after the static contract is proven should Core consider a dynamic activation Port. Its contract must define:

- Tool, MCP, and Skill as source-specific variants of one activation concept;
- bounded catalog summaries without complete Schemas or backend IDs;
- who authorizes activation and which Groups, Server Tools, Skills, or Resources are eligible;
- maximum Groups, Tools, Skill Resources, Instruction bytes, Tool Definition bytes, and MCP Catalog bytes per activation;
- next-turn-only activation through a new immutable Snapshot;
- deterministic Snapshot/Definition digests for approval and recording;
- cancellation, rejection, failure, stale-catalog, and suspension semantics;
- how Retry/Fallback reconstructs the same active set.

The active Tool set must never mutate during a Tool Batch or halfway through a physical Model Call. A dynamic activation request must be represented as an explicit state transition with a new immutable Snapshot.

See [Dynamic Capability Activation](./dynamic-capability-activation.md) and [ADR 0019](./adr/0019-dynamic-capability-activation.md) for the v0.2.0 design draft.

## Acceptance Criteria

1. `Toolkit.SnapshotGroups()` with no requested Groups produces an empty View.
2. An Agent that omits `Config.ToolGroups` keeps the compatibility default and sends all registered Tool Definitions.
3. A Run with selected Groups sends only the union of those Groups, with stable ordering and unchanged Schemas.
4. Context planning counts the selected Definitions and fails closed when the complete Request does not fit.
5. Approval digests and Recorder evidence remain bound to the exact Snapshot used for the Tool Batch.
6. A Retry and Fallback cannot silently add Tools.
7. Tests prove that changing the source Toolkit after Run start does not change the active View.
8. Logs and Metrics report bounded aggregate counts only; Tool arguments, Schema bodies, Scope keys, and credentials are excluded.

## Rejected Approaches

- Truncating or summarizing JSON Schema before sending it to the Model: changes the executable contract.
- Automatically enabling Tools based on model intent: hides a capability/permission transition and makes audit difficult.
- Mutating the Toolkit while a Batch is executing: invalidates Approval digests and replay diagnosis.
- Treating MCP reconnect as implicit activation refresh: changes active Tool definitions without a state transition.
- Injecting Skill instructions dynamically without provenance: changes prompt authority without auditable evidence.
- Exposing every Tool name and full Schema in a bootstrap prompt: retains the context and selection problems this plan is intended to solve.
- Treating disclosure as a security boundary: capability visibility is not authorization.

## Operational Guidance

Applications should choose Groups at the product boundary from user intent, tenant policy, and trusted workflow state. They should log the selected Group and Snapshot digest through an authorized audit channel, while relying on the Runtime Manifest for content-free request-shape diagnostics. Large reference data belongs in bounded Tool Results or Artifacts, not in Tool descriptions or Schemas.
