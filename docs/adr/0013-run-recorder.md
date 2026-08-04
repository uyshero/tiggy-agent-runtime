# ADR 0013: Versioned Integrity-linked Run Recorder

- Status: Accepted
- Date: 2026-08-04

## Context

Logs, metrics, and traces optimize operational analysis but may be sampled, aggregated, rotated, or exported independently. Reliability evaluation and post-incident diagnosis need a portable per-Run Event journal with explicit completeness, privacy, ownership, and corruption detection. A completed-only snapshot would lose all evidence on process interruption, while calling the existing Event array a replay log would overstate what can safely be reproduced after external effects.

## Decision

1. Add a standard-library-only `record` package that consumes the existing Version 1 Event protocol without changing Agent configuration or State.
2. Append one versioned Entry per Event rather than waiting for terminal completion, so structurally valid partial Runs remain inspectable.
3. Link Entries per Run with SHA-256 over the complete Entry content and prior digest.
4. Validate contiguous Sequence, unique Event IDs, prior Parent references, a single first `run.started`, and terminal-last behavior.
5. Distinguish `Validate` for partial journals from `ValidateComplete` for terminal Runs.
6. Use `SafeSanitizer` by default: remove streamed model content/Tool fragments/provider bodies and unknown payloads. Require explicit `FullSanitizer` for raw payload retention.
7. Permit custom Sanitizers to change only Payload and contain Sanitizer/Journal panic as Sink failure.
8. Bound active Runs, per-Run Events, and sanitized Event bytes.
9. Provide a bounded non-evicting process-local Memory Journal and a caller-owned JSONL Journal/Reader with bounded decoding.
10. Keep direct recording strict. Applications may explicitly wrap it in `observe.BestEffort`, accepting an incomplete or missing audit trail.
11. Make no cryptographic-authenticity, persistence, fsync, database, retention, authorization, deletion, or re-execution claim.

## Consequences

- Tests and offline evaluation can consume one stable, validated format.
- Crash diagnostics can distinguish a valid partial chain from a complete Run.
- Event content changes, removal, insertion, and reordering break the chain unless every later digest is recomputed.
- Digest recomputation remains possible for a writer with journal access; Enterprise tamper evidence needs signatures/MAC and protected storage.
- Synchronous strict recording can add latency or fail a Run, which is necessary when recording completeness is a requirement.
- JSONL is portable but the application must define real storage durability and lifecycle.

## Rejected Alternatives

- **Store only `Result.Events`:** unavailable when the process exits before Result delivery and not an incremental persistence boundary.
- **Write only after terminal:** loses partial evidence and requires unbounded in-memory Event retention.
- **Treat Trace export as recording:** sampling and exporter policy do not guarantee a complete per-Run journal.
- **Record raw payloads by default:** unnecessarily retains streamed prompts, reasoning, Tool arguments, and provider details.
- **Automatically retry Journal writes:** an error after partial acceptance makes append side effects ambiguous.
- **Replay Models and Tools:** non-determinism, credentials, cost, permissions, and external side effects require a separate explicit system.
- **Add a database to Core:** couples the embeddable SDK to Enterprise durability, tenant, query, and retention policy.
