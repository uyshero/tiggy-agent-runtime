# ADR 0018: Progressive Capability Disclosure

- Status: Proposed
- Date: 2026-08-05

## Context

Tool Definitions and JSON Schemas consume the Model context window and influence the Model's choice space. Exposing every registered, MCP-discovered, or Skill-provided Tool at Run start creates avoidable token cost and increases the impact of an incorrect call. Runtime already has Toolkit Groups, immutable Run snapshots, bounded discovery, Permission, Approval, and content-free diagnostics.

## Proposal

1. Use explicit Group selection before Run as the first progressive-disclosure mechanism.
2. Keep the selected Toolkit View immutable for the Run and stable across physical Model Calls, Retry, Fallback, and Approval.
3. If an application needs discovery, expose bounded summaries without complete Schemas or backend identifiers.
4. Treat activation as a capability transition, not as authorization. Permission and Approval remain required before effects.
5. Defer dynamic activation until a separate protocol defines authorization, limits, Snapshot Digests, next-turn semantics, failure, suspension, and reconstruction.

## Compatibility

This proposal is additive. Existing callers that omit `ToolGroups` retain their current behavior. The v0.1.2 implementation is limited to documentation, tests, examples, and diagnostics; it does not add a catalog API, mutate a Toolkit during a Run, or change the default exposure set. Bounded catalog summaries are deferred to the dynamic activation protocol.

## Decision Gate

Accept this ADR only after v0.1.2 tests demonstrate stable Group projection, schema accounting, immutable snapshots, and no silent Tool expansion on Retry/Fallback. Reject any implementation that truncates or rewrites Schema bodies before Provider invocation.
