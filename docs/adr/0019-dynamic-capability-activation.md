# ADR 0019: Dynamic Capability Activation

- Status: Proposed
- Date: 2026-08-05

## Context

Static Tool Group and Skill activation keeps v0.1.x reliable because each Run
uses an immutable capability snapshot. However, long tasks may discover that
they need additional Tool, MCP, or Skill capabilities after the first Model
Call. Enabling them implicitly would change the Model context, approval
digests, retry/fallback reconstruction, Recorder evidence, and MCP/Skill
provenance without a clear state transition.

## Proposal

1. Treat dynamic activation as an explicit capability state transition, not as
   model-selected Tool registration.
2. Support Tool, MCP, and Skill activation through one bounded activation
   concept with source-specific evidence and limits.
3. Require application-owned authorization before activation completes. Human
   approval may suspend one invocation; suspension is not task failure.
4. Apply activation only to the next Model Call by constructing a new immutable
   capability snapshot and recording content-free lifecycle evidence.
5. Bind approval, retry, fallback, and Recorder diagnostics to snapshot and
   Tool definition digests.
6. Keep Permission and Approval as independent effect gates. Activation changes
   visibility, not authority to execute side effects.

## Compatibility

This ADR does not change v0.1.x behavior. Existing static `ToolGroups`, MCP
Tool projection, and Skill activation remain the supported mechanism until a
v0.2.0 protocol is implemented and tested.

## Required Before Acceptance

- A concrete `ActivationRequest` / `ActivationDecision` contract.
- Event payloads for requested, approved, rejected, failed, canceled, and
  applied activation transitions.
- Snapshot digest and definition digest rules across Tool, MCP, and Skill
  sources.
- Tests proving next-turn-only activation, no mid-batch mutation, Retry/Fallback
  reconstruction, stale MCP catalog rejection, Skill instruction provenance, and
  approval binding.
- Documentation that activation is not authorization and does not provide
  process-crash durability or exactly-once external effects.

## Rejected Alternatives

- Let the model directly enable Tools from a hidden catalog.
- Mutate the active Toolkit or MCP definitions during a physical Model Call.
- Treat MCP reconnect as automatic activation refresh.
- Dynamically inject Skill instructions without activation provenance.
- Use Recorder events alone as a recovery source.
