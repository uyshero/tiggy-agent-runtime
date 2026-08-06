# Observation Semantics

## Boundary

`observe` converts the existing Version 1 lifecycle Event stream into logs and metrics without receiving mutable Agent State or changing runtime decisions. Applications pass an Observer Sink to `RunStream` or `Continue`; Agent configuration and the `event` foundation protocol do not import logging or telemetry frameworks.

The Event Sink remains synchronous. A Sink passed directly to Agent is strict: error or panic becomes an Event Sink failure and stops delivery. `observe.BestEffort` contains both error and panic, calls an optional diagnostic handler, and returns nil. Use strict delivery for audit/recording requirements and best effort for operational logging and telemetry that must not fail a Run.

`observe.Chain` invokes non-nil Sinks in order. Every Sink receives an owned Event/Payload copy, so one Observer cannot mutate the next Observer's input. Chain stops at the first strict error. These helpers start no goroutines, own no queues, and cannot preempt a blocking callback.

`observe.AsyncSink` is the official bounded asynchronous wrapper for operational telemetry. It owns a single worker, clones Events on ingress, supports a queue size, `drop_newest`, `drop_oldest`, or `block` overflow policy, and exposes `Flush(ctx)` plus `Shutdown(ctx)` so applications can drain accepted Events during graceful shutdown. Drop policies report overflow through `OnError` and return nil; `block` preserves backpressure and can still block the Run when the queue is full. AsyncSink cannot preempt a delegate Sink that is already blocked; it only moves delivery behind a bounded queue. Strict audit and required recording should keep using direct synchronous Sinks.

Queue Size bounds the number of buffered Events, not the total retained bytes. AsyncSink does not inspect or reject Event payload size; upstream Runtime limits, Recorder sanitization limits, provider adapters, and application-specific Sink policy still own byte budgets.

`AsyncSink.Abort()` is the explicit non-waiting exit path. It immediately stops accepting new Events and asks the worker to discard queued Events, but cannot preempt a delegate invocation already in progress. If that callback never returns, its worker goroutine may remain until process exit. `Abort` itself returns without waiting; a later `Shutdown(ctx)` may be used to wait for cleanup.

`NewAsyncBatchSink` accepts a `BatchSink` and coalesces only Events already available in the queue, up to the configured Batch Size. It never waits on a batching timer, so low-volume traffic is delivered immediately as one-Event batches. Batch Size defaults to 64, is capped by configuration to the Queue Size, and explicit values larger than the Queue Size are rejected. A BatchSink error or panic is reported once through `OnError`; the Events count as processed because the delegate was invoked. If a BatchSink partially writes before returning an error, Core cannot determine the committed subset and does not retry the batch.

`AsyncSink.Stats()` returns a concurrency-safe, content-free snapshot containing queue/batch capacity, pending submissions, submitted/processed Event counts, delegate call count, newest/oldest drops, abort discards, delegate failure count, rejected submissions, and closed/aborted state. `Processed` counts Events passed to a delegate; `DelegateCalls` and `DelegateFailures` count callback invocations, including one invocation for a multi-Event batch. Queue depth counts buffered Events only; pending also includes the Event or batch currently being processed and an `AsyncBlock` submission waiting for capacity. After `Abort`, pending may remain non-zero until the current delegate returns and the worker discards its queue. The snapshot intentionally retains no Event, payload, error text, identifier, or timestamp. Detailed failures remain transient through `OnError`; applications may map the numeric snapshot to their own metrics without emitting it through the same AsyncSink.

New Event types are forward-compatible. LogSink records only their envelope, while Metrics and OTel ignore unknown payloads until an explicit privacy and cardinality mapping exists.

## Structured Logging

`observe.LogSink` writes `slog.Record` values using the Event timestamp and default levels:

- operation starts and deltas: Debug;
- normal outcomes and completed Runs: Info;
- cancellation, retry, fallback, and limits: Warn;
- failed Runs, Runtime errors, and failed operations: Error.

The Logger receives correlation IDs, Event type/sequence, bounded operation metadata, token counts, and safe `event.ErrorPayload` fields. It never logs the raw Event payload. In particular, Model Delta text, reasoning, Tool argument fragments, Message content, Artifact bodies, Memory text, Scope keys, and provenance IDs are excluded. Applications still own Logger redaction, output destination, retention, and access control.

For `model.call.started`, LogSink includes selected fixed Context Manifest aggregates such as projected/final Message count, Instruction Stack count/bytes, fixed Instruction Stack kind bytes, Memory/Compression projection count, Text bytes, Artifact blocks, and Tool Definition count/bytes. It never logs the corresponding bodies or IDs. See [Context diagnostics](./context-diagnostics.md).

## Metrics

`observe.Metrics` consumes a complete Event stream beginning at `run.started`, correlates paired operations by Event parent ID, and records:

- Run count, duration, token usage, and cost micros;
- Model and Tool call count/duration;
- Context preparation and compression count/duration;
- Artifact offload and Memory operation count/duration;
- Retry, fallback, and limit counts.

Metrics use Tiggy-owned `Measurement`, `Attribute`, and `Recorder` contracts. Duration uses milliseconds. A Metrics value is concurrency-safe across Runs and removes Run/operation correlation state at terminal events, including incomplete operation state on abnormal termination.

Metric attributes intentionally exclude Run ID, Event ID, Call ID, Memory namespace/Scope key, error code, and free-form reason. The bounded dimensions are Agent name, configured Model identity, configured Tool name, operation/status, usage source, token type, context pressure, compression strategy, boolean Tool error, and error class. Deployments must still bound the number of configured Agent, Model, Tool, and strategy names.

`observe.NewAsyncMetrics` maps one AsyncSink to the same Recorder contract. `Record()` emits queue capacity, effective Batch Size, Queue Depth, Pending, Closed, and Aborted as Gauges; monotonic submitted/processed/delegate/drop/failure/rejected values are emitted as Counter deltas since the previous sample. A required, bounded `observer.name` identifies a configured Sink without using Run/Event IDs. AsyncMetrics owns no timer or goroutine: the application chooses its scrape interval and shutdown sample. Its baseline advances before Recorder I/O, so a partial Recorder failure is not retried and may lose that sample rather than duplicate already accepted Counter deltas.

## OpenTelemetry Adapter

`observe/otel` uses the official OpenTelemetry Go API. It maps Run and paired Model, Tool, Context, Compression, Artifact, and Memory operations to timestamped parent/child spans, and exports the same Metrics measurements through OTel instruments. Delta bodies are not attached to spans.

The constructor requires application-owned `trace.TracerProvider` and `metric.MeterProvider`. The Adapter does not create Exporters, start network clients, flush, or call Provider `Shutdown`. The application configures resources, sampling, readers/exporters, batching, timeouts, and shutdown order. `Observer.NewAsyncMetrics` binds an AsyncSink sampler to the same MeterProvider and maps `MetricGauge` to synchronous OTel Gauges. An optional concurrency-safe `ParentContext` can connect each Run span to an inbound trace; its error conditions and panic remain Observer failures and can be isolated with `BestEffort`.

OTel span attributes may contain Run/Event correlation IDs and safe error codes because traces are diagnostic records, not metric dimensions. They still exclude raw prompts, model deltas, Tool arguments/results, Memory content, Scope keys, and Artifact bodies.

## Example

```go
logSink, err := observe.LogSink(logger, observe.LogOptions{})
if err != nil {
    return err
}
telemetry, err := tiggyotel.New(tiggyotel.Options{
    TracerProvider: tracerProvider,
    MeterProvider:  meterProvider,
})
if err != nil {
    return err
}
telemetryAsync, err := observe.NewAsyncSink(telemetry.Sink, observe.AsyncOptions{
    QueueSize:  1024,
    DropPolicy: observe.AsyncDropNewest,
    OnError:    reportObserverError,
})
if err != nil {
    return err
}
asyncMetrics, err := telemetry.NewAsyncMetrics("otel.primary", telemetryAsync)
if err != nil {
    return err
}

sink := observe.Chain(
    observe.BestEffort(logSink, reportObserverError),
    telemetryAsync.Sink,
)
result, err := runtime.RunStream(ctx, input, sink)
if err := telemetryAsync.Shutdown(shutdownCtx); err != nil {
    reportObserverError(err)
    telemetryAsync.Abort()
}
if err := asyncMetrics.Record(); err != nil {
    reportObserverError(err)
}
stats := telemetryAsync.Stats()
```

For exporters with a native batch API:

```go
telemetryAsync, err := observe.NewAsyncBatchSink(
    func(events []event.Event) error {
        return exporter.Export(events)
    },
    observe.AsyncBatchOptions{
        QueueSize:  1024,
        BatchSize:  64,
        DropPolicy: observe.AsyncDropNewest,
        OnError:    reportObserverError,
    },
)
```

AsyncSink has stress and benchmark coverage in the `observe` package:

```bash
go test ./observe -run 'TestAsyncSinkStressAccounting'
go test ./observe -run '^$' -bench 'Benchmark(DirectSink|AsyncSink|AsyncBatchSink|AsyncSinkParallel|AsyncBatchSinkParallel)$' -benchmem
```

The diagnostic error handler must not emit through the same failing Sink. Best effort contains failures but does not make a blocking observer asynchronous; use `AsyncSink` when ordinary telemetry needs bounded queueing.
