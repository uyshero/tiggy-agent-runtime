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
- [x] Read and Write tools
- [x] Edit, Glob, and Grep tools
- [x] Bash tool and CommandRunner boundary
- [x] MCP transport and lifecycle
- [x] External MCP compatibility suite
- [x] Skill discovery and activation

## P3: Context, Memory, and Observability

- [x] Token budget planner
- [x] Semantics-preserving compression
- [x] Compression provenance and fail-closed fallback
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
- Enterprise completion and approval workflows;
- RAG pipelines and vector-store product APIs;
- TTS and realtime voice;
- multi-agent teams;
- generated OpenAPI clients.

Deferred items require separate product requirements and architecture decisions. They are not implicit Core commitments.
