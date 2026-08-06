# Context Governance

## Goal

Tiggy Agent Runtime treats context governance as an operational reliability
problem, not as prompt stuffing. Every model call should make three things
clear:

1. what must always be preserved;
2. what may be compressed, offloaded, recalled on demand, or dropped;
3. how to diagnose the final request shape without logging sensitive bodies.

This document connects the existing Context Assembly, Memory, Artifact,
Compression, Event, Recorder, and `agenttest` contracts into one design view.
It does not add database, Worker, tenant-policy, or durable-memory commitments
to Core.

## Layers

| Layer | Meaning | Runtime mechanism | Rule |
|---|---|---|---|
| Fixed instructions | System rules, project rules, task rules, policies, active Skill instructions | `SystemPrompt`, `agent.Config.Instructions`, Skill activation, Instruction Stack | Always preserve above the dynamic-context boundary |
| Structured task state | Goal, plan, checklist, suspension/resume state, pending Tool Batch | `agent.State`, `StatusSuspended`, `Suspension`, `builtin.todo`, Tool Call/Result pairing | Keep structured; do not hide critical state only in old dialogue |
| Short-term working memory | Recent turns, active Tool relationships, compression summaries | `contextmanager.Planner`, `Compressor`, full State plus request-only compressed view | Compress request view, never rewrite full State |
| External long-term memory | User preferences, project knowledge, historical evidence, large Tool results | `memory.Port`, `artifact.Offloader`, application-owned stores | Recall only relevant slices; keep complete bodies outside Core |

The point of these layers is practical: when a task degrades, the application
can distinguish instruction growth, Tool schema growth, excessive Memory
recall, compression distortion, and oversized Tool results.

## Fixed instructions

Fixed instructions enter the Instruction Stack:

```text
SystemPrompt
Configured Instructions
Activated Skill instructions

=========== DYNAMIC CONTEXT BELOW ===========
```

The boundary tells the model that content below it is runtime evidence and
should not override higher-authority rules.

Relevant APIs and diagnostics:

- `contextassembly.Render`
- `agent.Config.SystemPrompt`
- `agent.Config.Instructions`
- Skill activation
- `model.call.started.context.InstructionStackBytes`
- `agenttest.ValidateInstructionStackBudget`

Fixed instructions should not be rewritten by compression. Diagnostics expose
aggregate byte counts and built-in segment kinds, not instruction bodies,
sources, or Skill names.

## Structured task state

Critical task state should be machine-readable where possible:

- terminal state: `completed`, `failed`, `canceled`, `suspended`;
- approval suspension: `SuspensionID`, pending Tool Batch, digest validation;
- Tool relationships: Tool Calls and Tool Results must remain paired;
- checklist state: `builtin.todo` state and `ExtractLatestTodoState`.

`run.suspended` is the terminal status of one invocation, not a business-task
failure. It means the Runtime stopped safely before external effects and is
waiting for an application or human decision before `Resume`.

## Short-term working memory

Compression changes the provider request view, not the complete Agent State.

The Compressor contract exists because arbitrary Message replacement is unsafe:
current active work and Tool Call/Result relationships must remain protected,
coverage must be explicit, and the compressed view must be replanned.

Relevant pieces:

- `contextmanager.Planner`
- `contextmanager.Compressor`
- `context.prepared`
- `context.compression.*`
- `evaluation/compression`

Goals, constraints, and current task state should remain high priority. Old
dialogue may become structured summaries. Repetitive logs and redundant output
should be dropped or offloaded first.

## External long-term memory

Long-term memory belongs outside Core because storage, tenant isolation,
deletion, encryption, and compliance differ by application.

Core defines the boundary:

- `memory.Port`
- `memory.Scope`
- `memory.Options`
- `artifact.Offloader`

Memory Recall is request-only: it enters the current model request, is not
appended to `Agent.State`, and emits only bounded metadata. Artifact references
keep large Tool Results out of the prompt while binding the reference to a
digest and application-owned storage object.

## Budgets, checkpoints, and references

Context governance needs waterlines:

| Signal | Purpose | Mechanism |
|---|---|---|
| Token budget | Check provider window fit | `context.prepared`, `TokenPlanner` |
| Content-free manifest | Observe request shape without bodies | `model.call.started.context`, `observe.LogSink` |
| Test budget guard | Prevent regressions | `agenttest.ValidateInstructionStackBudget`, `agenttest.ValidateContextBudget` |

Useful test budgets include fixed-instruction bytes, Tool Definition bytes and
count, Memory projection count, compression-summary count, request-message
count, Tool Result / Artifact blocks, text bytes, and Tool argument bytes.

Core checkpoints are process-local and verifiable: `State`, `Suspension`,
Events, and Recorder journals. Durable checkpoints such as database Run State,
Worker leases, approval records, Artifact stores, and Memory stores belong to
the Enterprise/application layer.

## Diagnostic path

For an abnormal model call, inspect:

1. `memory.recall.*`
2. `context.prepared`
3. `context.compression.*`
4. `model.call.started.context`
5. `tool.result.offload.*`
6. `retry.scheduled` / `fallback.activated`
7. controlled-test `ScriptedModel.Calls()` when request bodies are truly needed

Production logs should not print full prompts, Memory bodies, Tool schemas, or
Tool Results.

## Anti-overengineering boundary

Core should continue not to provide:

- built-in databases;
- enterprise Workers;
- exactly-once external side effects;
- automatic MCP Tool replay after reconnect;
- Recorder-driven execution recovery;
- unbounded Tool / MCP / Skill schema exposure;
- long-term Memory bodies copied into full Agent State.

The practical rule is:

> Core makes a single Agent invocation bounded, explainable, verifiable, and
> budgetable. Enterprise code owns persistence, scheduling, tenant security,
> audit policy, and cross-process recovery.

