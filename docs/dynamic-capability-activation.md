# Dynamic Capability Activation

## Purpose

Dynamic Capability Activation is the future protocol for exposing additional
Tool, MCP, or Skill capabilities after a task has started. It is intentionally
deferred from the v0.1.2 static disclosure work because changing capabilities
mid-run affects context, approval, retry, fallback, recording, and replay
diagnostics.

The goal is not to let the model secretly add tools. The goal is to make every
capability expansion explicit, authorized, observable, bounded, and replayable.

## Capability Sources

The protocol should treat these as different sources of the same activation
concept:

| Source | What activation may expose | Special risk |
|---|---|---|
| Native Toolkit | selected Tool Groups | full JSON Schemas increase context and choice space |
| MCP | selected Server Tools from a bounded catalog snapshot | reconnects may change definitions or availability |
| Skill | selected instructions, resources, and declared Tool Groups | activation changes both prompt authority and tools |

Activation is not authorization. Permission Policy and Approval Policy still
run before any external effect.

## Proposed Flow

```text
bounded catalog evidence
        ↓
activation request
        ↓
application / policy / optional human approval
        ↓
new immutable capability snapshot
        ↓
next Model Call receives the expanded definitions/instructions
```

Activation takes effect only at the next Model Call. It never mutates a
physical Model Request already in flight and never changes the Tool set while a
Tool Batch is executing.

## State Machine

```text
stable_snapshot
  ├─ request_activation
  ▼
activation_pending
  ├─ rejected/canceled/failed -> stable_snapshot
  └─ approved
       ▼
pending_next_turn_snapshot
       ▼
stable_snapshot
```

`activation_pending` may suspend one `Run` invocation, but suspension is not a
task failure. A resumed invocation must validate that the approved activation
still matches the requested source, groups, definition digests, and limits.

## Required Contract

A dynamic activation protocol must define:

- activation request ID, source type, requested groups/resources/tools, reason,
  and bounded catalog evidence;
- who authorizes the activation and which capabilities are eligible;
- maximum activated groups, tools, Skill resources, instruction bytes, Tool
  definition bytes, and MCP catalog bytes;
- deterministic snapshot digest and per-definition digest after activation;
- whether prompt changes are allowed and where they enter the Instruction Stack;
- retry, fallback, resume, and recorder reconstruction rules;
- failure, cancellation, rejection, and stale-catalog behavior;
- content-free events and logs that explain what changed without retaining
  prompt bodies, Tool schemas, backend IDs, Skill resource bodies, or secrets.

## Non-Negotiable Invariants

1. A Model Call uses exactly one immutable capability snapshot.
2. A Tool Batch executes against the same snapshot that produced its Tool Calls.
3. Approval binds to the exact call arguments and Tool definition digest from
   the active snapshot.
4. Retry reconstructs the same active snapshot unless an explicit activation
   transition already completed before the retry request.
5. Fallback starts from full State but must reconstruct the same active
   capability snapshot for that turn.
6. MCP reconnect never silently replays a Tool Call or replaces a Tool
   definition used by an approved batch.
7. Skill activation cannot smuggle hidden authority: instructions, selected
   resources, declared Tool Groups, and digests are all explicit evidence.
8. Recorder can diagnose activation history, but replay still requires the
   application-owned sources to provide matching snapshots.

## Deferred Implementation Shape

v0.2.0 can start with a conservative Port, not a full dynamic plugin system:

```go
type ActivationPort interface {
    RequestActivation(context.Context, ActivationRequest) (ActivationDecision, error)
}
```

The Port is application-owned. Core owns validation, event ordering, immutable
snapshot construction, and next-turn semantics. Enterprise can implement tenant
policy, database state, human approval, MCP fleet catalogs, and audit storage
behind the Port without making Core depend on those systems.

## Rejected Shortcuts

- Letting the model directly enable arbitrary Tools.
- Appending new Tool definitions into the current provider request.
- Mutating Toolkit, MCP, or Skill state while a Tool Batch is executing.
- Treating MCP reconnect as permission to refresh active definitions silently.
- Injecting Skill instructions dynamically without a recorded activation event.
- Using Recorder alone as a recovery mechanism.

These shortcuts are tempting because they feel convenient. They make debugging
and replay miserable.
