# Runtime Reliability Evaluation

## Purpose

The reliability baseline turns Runtime guarantees into an executable gate. It runs deterministic, process-local scenarios through the public `agent`, `agenttest`, `contextmanager`, `memory`, and `toolkit` APIs, then checks the returned State, error classification, lifecycle trace, diagnostic evidence, and delegate call counts.

It answers a narrow question: when a known failure occurs, does the Runtime terminate predictably, preserve a valid State, explain the failure with stable evidence, and avoid an undeclared replay?

## Run It

The baseline needs no model credentials, database, network, or Enterprise service:

```bash
make eval-reliability
```

It writes:

- `reports/reliability-baseline.json` for CI and later comparison;
- `reports/reliability-baseline.md` for review.

Generated reports are ignored by Git and should be uploaded as CI artifacts when retention is needed. `make verify` also runs the baseline. The command exits with `0` when every scenario passes, `1` when an invariant fails, and `2` when a report cannot be encoded or written.

Custom output paths are available through the command directly:

```bash
go run ./cmd/tiggy-runtime-eval \
  -json /tmp/tiggy-reliability.json \
  -markdown /tmp/tiggy-reliability.md
```

## Current Scenarios

| Scenario | Expected outcome | Main proof |
|---|---|---|
| successful completion | `completed` | normal control path remains valid |
| model panic | `failed model/model_failed` | provider panic is contained |
| invalid model response | `failed model/invalid_model_response` | invalid output never enters State |
| Model Delta overflow | `failed limit/max_model_deltas` | streaming input is bounded |
| context overflow | `failed limit/context_overflow` | model is not called with an overflowing context |
| Provider context-length recovery | `completed` | one policy-authorized compression retry uses the reduced request view |
| repeated Provider context-length | `failed model/context_length` | recovery is limited to one compression and two Provider calls |
| Compressor panic | `failed internal/context_compression_failed` | compression extension panic is contained |
| Memory recall failure | `failed memory/memory_recall_failed` | fail-run policy is classified and observable |
| Tool panic | `failed tool_runtime/tool_runtime_failed` | unsafe details do not escape into the model path |
| Tool cancellation | `canceled canceled/tool_canceled` | cancellation closes the Tool lifecycle and Run |
| invalid Tool Result | `failed tool_runtime/tool_runtime_failed` | extension output is validated before State commit |
| Event Sink failure | `failed event_sink/event_sink_failed` | the internal trace still reaches one balanced terminal |

## Invariants

Every scenario must pass all five checks:

1. `terminal_invariant`: `agenttest.ValidateTrace` accepts the trace, including contiguous sequence numbers, balanced operation pairs, and exactly one terminal Event.
2. `state_invariant`: the returned State passes `ValidateComplete`, or `ValidateSuspended` for a suspended Run.
3. `classification`: Status and `RunError` class/code exactly match the declared outcome.
4. `diagnostics`: required lifecycle Events exist and `runtime.error` contains the same safe class/code.
5. `no_implicit_replay`: Model, Tool, Memory, Compressor, Token Counter, and Sink delegate calls do not exceed the scenario's declared maximum.

The report schema is versioned as `tiggy.runtime.reliability.v1`. A schema change requires an explicit version change or a backward-compatible additive field. Scenario inputs, IDs, clocks, failures, and expected outcomes are deterministic. `generated_at`, `duration_micros`, goroutine scheduling, and report file bytes are not benchmark promises and may vary between runs.

## Adding a Scenario

Add a scenario when a new failure boundary or execution invariant is introduced. Use only public Runtime APIs and deterministic test doubles. Declare the exact expected Status, error class/code, required diagnostic Events, and maximum delegate calls. The scenario must not require a real Provider, network, database, sleep-based timing, or Enterprise type.

This suite is not a latency benchmark, load test, quality evaluation of model answers, database durability test, process-crash recovery test, or distributed chaos environment. Those are separate evaluation layers and should not weaken this fast local gate.

Provider HTTP/SSE classification is a separate deterministic layer invoked with `make eval-provider-reliability`. Its fixtures include success, HTTP authentication/rate-limit/quota/context-length/unavailable failures, malformed or interrupted streams, incomplete streams, safety filtering, request deadlines, Delta backpressure, and failure Request ID propagation. The explicitly billed external smoke check remains `make provider-live`. See [Live Provider contract verification](./live-provider.md).
