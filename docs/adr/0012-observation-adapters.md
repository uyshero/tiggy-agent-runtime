# ADR 0012: Read-only Observation Adapters

- Status: Accepted
- Date: 2026-08-03

## Context

The Runtime already emits a complete synchronous Event trace, but each application would otherwise invent logging fields, metric names, operation pairing, privacy filters, and OTel span topology. Putting `slog` or OpenTelemetry types into Agent/event contracts would couple the embeddable Core protocol to one telemetry stack. Treating monitoring failure like durable audit-delivery failure would also make an exporter outage terminate healthy Agent Runs.

## Decision

1. Add an independent `observe` package that consumes owned Event copies and never receives mutable Agent State.
2. Keep Agent and foundation Event contracts unchanged. Applications compose Observer Sinks at `RunStream`/`Continue`.
3. Keep direct Sink delivery strict. Add explicit `BestEffort` containment for operational observers whose errors and panics must not affect Run execution.
4. Add a standard-library `slog` Sink that uses Event timestamps and selected metadata but never logs raw payload bodies.
5. Add a backend-neutral, concurrency-safe Metrics observer with Tiggy-owned Measurement/Recorder values and paired-event duration tracking.
6. Exclude high-cardinality correlation IDs, Scope/Namespace, error codes, and free-form reasons from metric attributes.
7. Add `observe/otel` using official OpenTelemetry Go v1.44.0 API/SDK modules. Third-party types remain inside the optional Adapter package.
8. Build timestamped Run and operation spans from Event parent relationships. Ignore Model/Tool Delta bodies.
9. Require application-owned TracerProvider and MeterProvider. The Adapter owns no Exporter, goroutine, network connection, flush, or shutdown lifecycle.
10. Permit an explicit ParentContext resolver for inbound trace correlation and contain its panic at the Observer boundary.
11. Treat new Event types as forward-compatible: log only their envelope and ignore unknown metric/trace payloads until reviewed mappings exist.

## Dependency Assessment

- Capability: official OTel trace/metric export without reimplementing its API contracts.
- Alternative: a Tiggy-only trace exporter would not interoperate with OTel Providers, sampling, processors, or semantic tooling.
- License: Apache-2.0.
- Ownership/maintenance: maintained by the OpenTelemetry project and CNCF; v1 API is stable.
- Security: verified through the repository vulnerability scan; no current reachable advisory was found at acceptance.
- Impact: API, trace, metric, SDK, and their small logging/auto-SDK/hash transitive modules. SDK modules are used by conformance tests; production applications select concrete Providers/Exporters.
- Resources: the API and Adapter start no goroutines. Application-selected SDK processors/exporters may own goroutines, queues, network resources, and shutdown duties.
- Public surface: OTel types appear only in `observe/otel`, never in Agent, Event, or other foundation signatures.
- Replacement: applications may use `observe.Metrics`, `LogSink`, or another Event Sink without importing `observe/otel`.

## Consequences

- Core provides consistent logs, metrics, and traces while preserving backend choice.
- Applications explicitly choose whether an observation failure is fatal or best effort.
- Metrics remain suitable for aggregation; trace/log records retain richer correlation metadata.
- A Sink callback is still synchronous. Best effort contains failure but does not solve blocking or backpressure.
- This is lifecycle observability, not durable Run recording, audit storage, or crash recovery.

## Rejected Alternatives

- **Put Observers in Agent Config:** hides per-Run delivery policy and complicates embedding multiple consumers.
- **Always swallow observer failures:** breaks strict audit/delivery use cases and hides loss.
- **Always fail the Run:** lets a monitoring outage become an execution outage.
- **Log every Event payload:** leaks streamed content, Tool arguments, Memory metadata, and large values.
- **Use Run IDs as metric labels:** creates unbounded cardinality.
- **Create global OTel Providers:** takes lifecycle and configuration ownership away from the embedding application.
- **Implement an OTel-compatible protocol locally:** duplicates a stable official API and risks semantic incompatibility.
