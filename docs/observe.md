# Observation Semantics

## Boundary

`observe` converts the existing Version 1 lifecycle Event stream into logs and metrics without receiving mutable Agent State or changing runtime decisions. Applications pass an Observer Sink to `RunStream` or `Continue`; Agent configuration and the `event` foundation protocol do not import logging or telemetry frameworks.

The Event Sink remains synchronous. A Sink passed directly to Agent is strict: error or panic becomes an Event Sink failure and stops delivery. `observe.BestEffort` contains both error and panic, calls an optional diagnostic handler, and returns nil. Use strict delivery for audit/recording requirements and best effort for operational logging and telemetry that must not fail a Run.

`observe.Chain` invokes non-nil Sinks in order. Every Sink receives an owned Event/Payload copy, so one Observer cannot mutate the next Observer's input. Chain stops at the first strict error. These helpers start no goroutines, own no queues, and cannot preempt a blocking callback.

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

## OpenTelemetry Adapter

`observe/otel` uses the official OpenTelemetry Go API. It maps Run and paired Model, Tool, Context, Compression, Artifact, and Memory operations to timestamped parent/child spans, and exports the same Metrics measurements through OTel instruments. Delta bodies are not attached to spans.

The constructor requires application-owned `trace.TracerProvider` and `metric.MeterProvider`. The Adapter does not create Exporters, start network clients, flush, or call Provider `Shutdown`. The application configures resources, sampling, readers/exporters, batching, timeouts, and shutdown order. An optional concurrency-safe `ParentContext` can connect each Run span to an inbound trace; its error conditions and panic remain Observer failures and can be isolated with `BestEffort`.

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

sink := observe.Chain(
    observe.BestEffort(logSink, reportObserverError),
    observe.BestEffort(telemetry.Sink, reportObserverError),
)
result, err := runtime.RunStream(ctx, input, sink)
```

The diagnostic error handler must not emit through the same failing Sink. Best effort does not make a blocking observer asynchronous; bounded asynchronous export remains an application/Provider responsibility.
