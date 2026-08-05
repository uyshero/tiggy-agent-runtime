# Roadmap

## P0: Protocol Foundation

- [x] Independent Go module and CI baseline
- [x] Message and content protocol
- [x] Tool Call and Tool Result protocol
- [x] Provider-neutral model stream
- [x] Usage and Provider Error protocol
- [x] Versioned runtime event envelope
- [x] Dependency and engineering standards
- [x] Protocol JSON round-trip fixtures
- [x] Compatibility changelog

## P1: Reliable Agent Runtime

- [x] `agent.New`, `Run`, and `RunStream`
- [x] Agent state and context projection
- [x] Bounded ReAct loop
- [x] Exactly one terminal event
- [x] Tool Call/Result invariant validator
- [x] Cancellation and deadline propagation
- [x] Retry and fallback policy
- [x] Fake Model, Fake Tool, and trace assertions
- [x] Panic containment and owned timer cleanup tests

## P2: Toolkit and Execution Environment

- [x] Native Tool contract and Toolkit
- [x] JSON Schema validation decision
- [x] Tool namespaces and groups
- [x] Sequential and concurrency-safe batches
- [x] SafeSpace
- [x] Basic permission policy
- [x] Tool approval suspension and exact-call resume
- [x] Read and Write tools
- [x] Edit, Glob, and Grep tools
- [x] Bash tool and CommandRunner boundary
- [x] MCP transport and lifecycle
- [x] External MCP compatibility suite
- [x] Skill discovery and activation

## P3: Context, Memory, and Observability

- [x] Token budget planner
- [x] Context Assembly and Instruction Stack boundary
- [x] Semantics-preserving compression
- [x] Compression provenance and fail-closed fallback
- [x] Explicit bounded Provider context-length compression recovery
- [x] Deterministic application-owned Compressor quality evaluation
- [x] Content-free per-Model-Call Context diagnostic manifest
- [x] Large Tool Result offload contract
- [x] Memory recall and observe
- [x] Noop and in-memory Memory implementations
- [x] Memory scope, expiry, provenance, and conflict policy
- [x] Logging, metrics, and OpenTelemetry observers
- [x] Run Recorder
- [x] Context and memory fault injection

## Deferred Until Required

- durable cross-process Agent execution;
- PostgreSQL state machine;
- Worker lease and distributed scheduling;
- exactly-once external tool effects;
- Enterprise completion gates and durable approval coordination;
- RAG pipelines and vector-store product APIs;
- TTS and realtime voice;
- multi-agent teams;
- generated OpenAPI clients.

Deferred items require separate product requirements and architecture decisions. They are not implicit Core commitments.

## Candidate: v0.1.2

- [x] Static progressive capability disclosure using existing Toolkit Groups and immutable Run snapshots
- [x] Deterministic zero/single/multi-Group schema exposure tests
- [x] Schema byte accounting through immutable Toolkit Views and content-free Context Manifests
- [x] Retry/Fallback tests proving that Tool exposure cannot expand silently
- [x] Context Assembly and Instruction Stack boundary with fixed static segment kinds
- [x] Instruction Stack aggregate and per-kind byte diagnostics in `model.call.started.context`
- [x] Agent test-kit example and helper for content-free Instruction Stack budget assertions
- [x] External consumer example focused only on asserting the exact exposed Tool set

Dynamic activation during a running Task is intentionally deferred until a separate capability-transition protocol is accepted. See [Progressive capability disclosure](./progressive-disclosure.md), [Dynamic Capability Activation](./dynamic-capability-activation.md), [ADR 0018](./adr/0018-progressive-capability-disclosure.md), and [ADR 0019](./adr/0019-dynamic-capability-activation.md).

## Draft: v0.2.0 Dynamic Capability Activation

- [ ] Activation request/decision contract covering Tool, MCP, and Skill sources
- [ ] Optional bounded catalog summaries without full Schema bodies or backend identifiers
- [ ] Content-free activation lifecycle Events and snapshot/definition digest rules
- [ ] Next-turn-only immutable snapshot transition
- [ ] Optional suspension/resume path for application or human authorization
- [ ] Retry/Fallback reconstruction tests for the same active capability set
- [ ] MCP stale-catalog and reconnect safety tests
- [ ] Skill instruction/resource provenance tests
