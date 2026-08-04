# Context and Memory Fault Injection

## Purpose and Boundary

Fault injection is deterministic test infrastructure in `agenttest`. It verifies that Agent failure classification, cancellation, degradation policy, paired lifecycle Events, terminal uniqueness, and no-replay rules survive failures at Context and Memory extension boundaries.

It is intentionally absent from `agent.Config` and production packages. Core does not randomly fail requests, read environment fault flags, add real delays, or expose a remote failure-control endpoint. Production chaos experiments must be an explicit application/Enterprise harness around the same Ports.

## Fault Points

One `FaultInjector` can wrap:

| Point | Wrapped contract |
|---|---|
| `context.plan` | `contextmanager.Planner` |
| `context.compress` | `contextmanager.Compressor` |
| `context.token_count` | `model.TokenCounter` |
| `memory.recall` | `memory.Port.Recall` |
| `memory.observe` | `memory.Port.Observe` |

Wrappers copy mutable Requests before delegation and copy Memory Recall results on return. They do not own or close delegates.

## Rules and Actions

A `FaultRule` targets an exact one-based Call ordinal at one Point. Rules are validated once and immutable. Calls without a matching rule pass through to the delegate.

- `return_error`: return a typed `FaultError` before calling the delegate;
- `panic`: panic with the configured value before delegation, exercising the Runtime panic boundary;
- `wait_for_cancel`: wait only on `ctx.Done()` and return `ctx.Err()` without a timer or goroutine;
- `invalid_result`: return a point-specific invalid value with nil error, exercising invariant validation;
- `error_after_delegate`: call the delegate once and, only if it succeeds, return a typed error to simulate an accepted side effect whose acknowledgement was lost.

Invalid Planner/Compressor results are zero values. Invalid Token Count is zero. Invalid Memory Recall contains an Item crossing the requested Scope. Memory Observe has no result, so `invalid_result` is rejected for that Point.

`error_after_delegate` never retries. For Memory Observe this deliberately proves why a transport error cannot establish whether the storage side effect occurred.

## Determinism and Cancellation

Call ordinals are monotonic per Point. One Injector is concurrency-safe, but concurrent calls at the same Point are ordered by mutex acquisition; deterministic scenarios should use one Injector per controlled flow rather than sharing it across unrelated concurrent Runs.

`Watch(point, call)` returns a channel closed as that invocation enters the Injector. Tests register Watch before starting a `wait_for_cancel` action, wait for the signal, then cancel the Context. This avoids sleeps and timing races. The Injector creates no background goroutines or timers.

## Example

```go
injector, err := agenttest.NewFaultInjector(agenttest.FaultRule{
    Point:  agenttest.FaultMemoryObserve,
    Call:   1,
    Action: agenttest.FaultErrorAfterDelegate,
    Err:    errors.New("acknowledgement lost"),
})
if err != nil {
    return err
}

faultyMemory, err := injector.WrapMemory(realisticTestMemory)
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    // ... deterministic Model and IDs ...
    Memory:      faultyMemory,
    MemoryScope: testScope,
})
```

The test should then assert `agenttest.ValidateTrace`, Run status/error code, injected call count, and delegate side-effect count. Triggering a fault without asserting terminal invariants is insufficient.

## Non-goals

The harness does not simulate packet-level corruption, CPU/memory pressure, process termination, database failover, clock skew, disk-full behavior, distributed partitions, or probabilistic latency. Those require purpose-built Adapter or system test environments. It also does not weaken Context/Memory production validation or promise that one synthetic fault models every backend failure.
