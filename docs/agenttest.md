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

These values have different meanings:

```text
provider.Calls()[n].Messages  exact view sent to the Model on call n
result.State.Messages         complete conversation State retained by Runtime
```

Compression may replace history only in the first value; full State remains unchanged. Recalled Memory also enters only the request view and is not appended to State.

This captures the Tiggy-owned request delivered to a Provider Adapter. It cannot show the final OpenAI, Anthropic, or other provider-specific HTTP JSON produced inside that Adapter. Inspect that wire format with a request-capturing fake HTTP server or Transport in the Adapter's own integration tests.

Full context can contain prompts, Tool Results, or recalled data. Print it only in controlled tests; do not copy this pattern into production logs.

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
| `EventTypes` / `DecodePayload` | focused Event assertions without ad hoc JSON handling |

`ScriptedModel.Assert`, `ScriptedModel.Calls`, `FuncMemory`, `FuncTool` arguments, and the Context/Artifact function adapters clone their owned boundary values. `FuncModel` is intentionally a raw `model.Model` callback and adds no extra copy; its implementation must obey the normal Model ownership contract.

## What to Assert

A useful Runtime test checks more than the returned text:

- the final `agent.Status` and, on failure, `agent.RunError.Class` plus `Code`;
- Model and side-effecting delegate call counts;
- the next Model Request contains the expected Tool Result, recalled Memory, or compressed context;
- `result.State.ValidateComplete()` when inspecting State directly;
- `agenttest.ValidateTrace(result.Events)` or the Events captured by `Trace`;
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
