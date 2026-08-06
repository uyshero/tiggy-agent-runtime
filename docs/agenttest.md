# Agent Test Kit

## Purpose

`agenttest` provides deterministic, protocol-aware test doubles for applications that embed Tiggy Agent Runtime. It is intended for `_test.go` files. It does not call a real model, start infrastructure, change production `agent.Config`, or replace provider/database wire-level integration tests.

Use it to test one controlled execution scenario at a time:

```text
scripted Model response -> Runtime decision -> Tool or extension -> next Model request -> terminal result
```

The complete compiled example is [examples/agenttest-basic/agent_test.go](../examples/agenttest-basic/agent_test.go). Run it with:

```bash
go test -v ./examples/agenttest-basic
```

## Basic Workflow

1. Describe each expected Model invocation with `ModelStep`.
2. Use the step's `Assert` callback to inspect the exact provider-neutral `model.Request`.
3. Build Tools or extension Ports with the small `Func...` adapters.
4. inject `SequentialIDs` and `StepClock` into `agent.Config`.
5. run the Agent with a `Trace` sink.
6. assert the business outcome, call counts, stable error class/code, and `ValidateTrace`.

`ScriptedModel` consumes one step per `Generate` call. An extra call fails with `agenttest: unexpected model call N`. `Calls` returns cloned requests, so tests can inspect them after the Run without sharing mutable Runtime values.

## Inspecting the Model Context

`ScriptedModel.Calls()` returns the exact provider-neutral `model.Request` received at every Model boundary. Log it after the Run to inspect the complete context actually projected for each call:

```go
for index, request := range provider.Calls() {
    data, err := json.MarshalIndent(request, "", "  ")
    if err != nil {
        t.Fatal(err)
    }
    t.Logf("Model Call #%d:\n%s", index+1, data)
}
```

Successful test logs are displayed with `-v`:

```bash
go test -v -run TestAgentWithScriptedModelAndTool ./examples/agenttest-basic
```

The captured Request includes the actual System/User/Assistant Messages, Tool Calls and Results, recalled Memory projection, compressed context view, Tool Definitions and Schemas, Model identity, output limit, Parameters, Run ID, and Call ID for that invocation.

The same Request can be inspected synchronously in a `ModelStep.Assert` callback:

```go
agenttest.ModelStep{
    Assert: func(request model.Request) error {
        data, err := json.MarshalIndent(request, "", "  ")
        if err != nil {
            return err
        }
        t.Logf("actual model context:\n%s", data)
        return nil
    },
    Response: response,
}
```

Use the same callback to test static progressive disclosure. Configure
`agent.Config.ToolGroups`, then assert the exact Tool definitions visible to the
model:

```go
agenttest.ModelStep{
    Assert: func(request model.Request) error {
        got := make([]string, len(request.Tools))
        for index, definition := range request.Tools {
            got[index] = definition.Name
        }
        want := []string{"crm.lookup_customer", "kb.search"}
        if !reflect.DeepEqual(got, want) {
            return fmt.Errorf("model-visible tools = %v, want %v", got, want)
        }
        return nil
    },
    Response: response,
}
```

The compiled `TestConsumerSelectsToolGroupsBeforeRun` example registers Tools
in several Groups, selects only `customer_read` and `knowledge_read`, and proves
that a write-capable Tool is not sent to the Model.

These values have different meanings:

```text
provider.Calls()[n].Messages  exact view sent to the Model on call n
result.State.Messages         complete conversation State retained by Runtime
```

Compression may replace history only in the first value; full State remains unchanged. Recalled Memory also enters only the request view and is not appended to State.

This captures the Tiggy-owned request delivered to a Provider Adapter. It cannot show the final OpenAI, Anthropic, or other provider-specific HTTP JSON produced inside that Adapter. Inspect that wire format with a request-capturing fake HTTP server or Transport in the Adapter's own integration tests.

Full context can contain prompts, Tool Results, or recalled data. Print it only in controlled tests; do not copy this pattern into production logs.

For production-safe shape diagnostics, decode the optional `ModelCallPayload.Context` manifest from `model.call.started`. It contains fixed aggregate counts and byte sizes but no Message IDs or bodies. See [Context diagnostics](./context-diagnostics.md).

```go
for _, value := range trace.Events() {
    if value.Type != event.ModelCallStarted {
        continue
    }
    payload, err := agenttest.DecodePayload[event.ModelCallPayload](value)
    if err != nil || payload.Context == nil {
        t.Fatal(err)
    }
    snapshot := payload.Context
    t.Logf(
        "instruction stack bytes: total=%d system=%d project=%d task_rule=%d policy=%d knowledge=%d skill=%d",
        snapshot.InstructionStackBytes,
        snapshot.InstructionStackSystemPromptBytes,
        snapshot.InstructionStackProjectBytes,
        snapshot.InstructionStackTaskRuleBytes,
        snapshot.InstructionStackPolicyBytes,
        snapshot.InstructionStackKnowledgeBytes,
        snapshot.InstructionStackSkillBytes,
    )
}
```

Unlike `ScriptedModel.Calls()`, this manifest is safe for normal diagnostic
logs because it does not contain Prompt text, Tool schemas, Message IDs, Memory
content, Skill names, or segment sources. The complete compiled example is
`TestInspectInstructionStackContextManifest` in
[examples/agenttest-basic/agent_test.go](../examples/agenttest-basic/agent_test.go).

Use `ValidateInstructionStackBudget` when a test should fail if static
instructions grow past an agreed budget:

```go
if err := agenttest.ValidateInstructionStackBudget(trace.Events(), agenttest.InstructionStackBudget{
    MaxBytes:             8_000,
    MaxSystemPromptBytes: 1_000,
    MaxProjectBytes:      2_000,
    MaxTaskRuleBytes:     1_000,
    MaxPolicyBytes:       1_000,
    MaxKnowledgeBytes:    2_000,
    MaxSkillBytes:        2_000,
}); err != nil {
    t.Fatal(err)
}
```

Zero means "no limit" for that field. The helper checks every
`model.call.started` manifest in the trace and returns an error if the manifest
is missing, invalid, or exceeds any configured ceiling. It does not read the
model Request body.

Use `ValidateContextBudget` when a test should also guard dynamic context
growth, such as Tool Definition bytes, Tool argument bytes, recalled Memory
projections, compression summaries, Tool Result blocks, Artifact references, or
overall request message counts:

```go
if err := agenttest.ValidateContextBudget(trace.Events(), agenttest.ContextBudget{
    InstructionStack: agenttest.InstructionStackBudget{
        MaxBytes: 8_000,
    },
    MaxToolDefinitionBytes:    12_000,
    MaxToolArgumentBytes:      4_000,
    MaxTextBytes:              24_000,
    MaxRequestMessages:        32,
    MaxProjectedMessages:      32,
    MaxToolDefinitions:        16,
    MaxRecalledMemoryMessages: 4,
    MaxCompressionSummaries:   2,
    MaxToolResultBlocks:       8,
    MaxArtifactBlocks:         8,
}); err != nil {
    t.Fatal(err)
}
```

This is useful as a regression guard for progressive disclosure and context
assembly work: old dialogue may be compressed, complete Tool Results may be
offloaded to Artifacts, and Memory may be projected only when relevant, but the
test can still prove that the final model-visible request stays within an
agreed shape budget. The complete compiled example is
`TestValidateContextBudgetForProjectedRequest` in
[examples/agenttest-basic/agent_test.go](../examples/agenttest-basic/agent_test.go).

## Test Doubles

| API | Use |
|---|---|
| `NewScriptedModel` / `ModelStep` | ordered Model responses, errors, deltas, and request assertions |
| `FuncModel` | custom Model behavior such as blocking, cancellation, or panic |
| `FuncTool` | small native Tool with a real definition and callback |
| `FuncMemory` | Memory Recall/Observe callbacks |
| `FuncTokenCounter` | provider-specific token-count behavior |
| `FuncPlanner` / `FuncCompressor` | Context planning and compression callbacks |
| `FuncOffloader` | Artifact offload decisions |
| `SequentialIDs` | stable IDs such as `run_1`, `msg_1`, and `event_1` |
| `StepClock` | stable timestamps advanced by a fixed duration per call |
| `Trace` | concurrency-safe Event capture through `Trace.Sink` |
| `ValidateTrace` | lifecycle, pairing, sequence, parent, and terminal-invariant validation |
| `ValidateInstructionStackBudget` | content-free static instruction byte-budget guard |
| `ValidateContextBudget` | content-free request-shape guard for instructions, Tool schemas, Memory, summaries, and blocks |
| `EventTypes` / `DecodePayload` | focused Event assertions without ad hoc JSON handling |

`ScriptedModel.Assert`, `ScriptedModel.Calls`, `FuncMemory`, `FuncTool` arguments, and the Context/Artifact function adapters clone their owned boundary values. `FuncModel` is intentionally a raw `model.Model` callback and adds no extra copy; its implementation must obey the normal Model ownership contract.

## What to Assert

A useful Runtime test checks more than the returned text:

- the final `agent.Status` and, on failure, `agent.RunError.Class` plus `Code`;
- Model and side-effecting delegate call counts;
- the next Model Request contains the expected Tool Result, recalled Memory, or compressed context;
- `result.State.ValidateComplete()` when inspecting State directly;
- `agenttest.ValidateTrace(result.Events)` or the Events captured by `Trace`;
- `agenttest.ValidateInstructionStackBudget` when static instruction growth is a regression risk;
- `agenttest.ValidateContextBudget` when Tool schemas, Memory projection, compression summaries, or Tool/Artifact blocks could silently expand the model request;
- a specific typed Event payload when the scenario concerns retry, compression, Memory, or Artifact behavior.

Use `DecodePayload[T]` for typed payload inspection:

```go
payload, err := agenttest.DecodePayload[event.MemoryPayload](value)
```

Do not assert generated Event IDs or timestamps unless they are relevant. Inject deterministic IDs/time so failure output remains reproducible, then assert protocol meaning.

## Failure Injection

`FaultInjector` wraps Context Planner, Compressor, Token Counter, and Memory boundaries. A `FaultRule` selects an exact one-based call ordinal and one action: return an error, panic, wait for cancellation, return an invalid result, or fail after a successful delegate call.

For cancellation, register `Watch(point, call)` before starting the Run, wait for the returned channel, and then cancel the Context. Do not coordinate cancellation tests with sleeps.

Use one Injector per controlled flow. Although the Injector is concurrency-safe, calls at the same Point are numbered by lock-entry order, so sharing it across unrelated parallel Runs makes scenario ownership unclear.

See [Context and Memory fault injection](./fault-injection.md) for the rule matrix and examples.

## Common Scenarios

| Scenario | Recommended API | Required assertions |
|---|---|---|
| successful answer | one `ModelStep` | completed status, one Model call, valid Trace |
| Tool round trip | two `ModelStep` values plus `FuncTool` | second Request has Tool Result, Tool called once |
| Provider failure | `ModelStep.Err` with `model.ProviderError` | stable RunError class/code, retry count, valid Trace |
| Model panic or cancellation | `FuncModel` | failed/canceled status and one terminal Event |
| Memory degradation | `FuncMemory` or wrapped `memory.Noop` | policy outcome and typed Memory Event payload |
| ambiguous side effect | `FaultErrorAfterDelegate` | delegate called once and no implicit replay |
| cancellation at a boundary | `FaultWaitForCancel` plus `Watch` | canceled status without timing sleeps |
| malformed extension output | `FaultInvalidResult` | fail-closed error code and balanced lifecycle Events |

## Scope

`agenttest` verifies Runtime orchestration and the Tiggy-owned contracts around an Adapter. It does not verify an OpenAI/Anthropic wire mapping, a PostgreSQL transaction, an MCP server implementation, process crash recovery, or network partitions. Test those in Adapter integration suites, then use `agenttest` to verify how the Runtime reacts to their normalized results and errors.
