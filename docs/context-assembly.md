# Context Assembly and Instruction Stack

## Purpose

Context Assembly makes the model request shape explicit without turning Core
into a policy engine. It separates stable authority from request-time evidence:

```text
Static Instructions
  - application SystemPrompt
  - explicitly activated Skill instructions and selected resources

=========== DYNAMIC CONTEXT BELOW ===========

Dynamic Context
  - recalled Memory evidence
  - context compression summaries
  - future bounded catalogs or task-state projections
```

The boundary is model-visible by design. It tells the model that content below
the line is contextual evidence, not authority to override static instructions.

## Runtime Behavior

When `SystemPrompt` or active Skills exist, Agent creates one internal System
Message rendered by `contextassembly.Render`. The message carries
`metadata.kind=instruction_stack`, records the static segment kinds, and records
aggregate text bytes for the built-in segment kinds. The complete rendered text
is stored in `Agent.State` only when a new conversation starts; `Continue`
reuses the existing State.

Applications can add extra static segments through `agent.Config.Instructions`:

```go
runtime, err := agent.New(agent.Config{
    Name:         "assistant",
    SystemPrompt: "Follow the runtime safety rules.",
    Instructions: []contextassembly.Segment{
        contextassembly.StaticSegment(
            contextassembly.KindProject,
            "repo/AGENTS.md",
            "Use the repository's existing style and tests.",
        ),
        contextassembly.StaticSegment(
            contextassembly.KindTaskRule,
            "task",
            "Keep changes focused on the requested behavior.",
        ),
        contextassembly.StaticSegment(
            contextassembly.KindPolicy,
            "security",
            "Do not expose secrets or credentials.",
        ),
    },
    Model: provider,
    ModelIdentity: identity,
})
```

The static render order is SystemPrompt, configured Instructions, then active
Skills. `agent.New` copies and validates the configured segments.

The fixed diagnostic kinds are:

- `system_prompt`
- `project`
- `task_rule`
- `policy`
- `knowledge`
- `skill`

The Model Context Manifest exposes their aggregate byte counts as metadata-only
budget signals. It does not expose segment text, source names, Skill names, or
custom kind names. Custom segment kinds may still render into the Instruction
Stack, but Core does not turn them into new protocol fields.

Memory recall remains request-only. It is inserted as a separate internal
System Message after the leading System Messages, so it appears below the
Instruction Stack boundary in the provider request view and never enters
`Agent.State`.

Compression summaries are also internal System Messages produced by the
Context Manager. They remain dynamic request-view artifacts and do not rewrite
the complete State.

## Non-Goals

This layer does not decide which policy wins, authorize tools, approve effects,
select models, persist memory, or rewrite Tool schemas. It only gives the
runtime and tests a deterministic place to assemble and explain model-visible
context.

Production applications should still keep tenant policy, approval identity,
durable memory, deletion, and audit storage outside Core.
