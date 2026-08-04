# ADR 0010: Process-local Memory Adapters

- Status: Accepted; conflict behavior extended by ADR 0011
- Date: 2026-08-03

## Context

The Memory Port needs deterministic implementations for disabling the capability, examples, Agent integration tests, and local development. Treating an in-memory map as production storage would overstate durability and consistency. Automatically storing every observed model response would also create an implicit fact-admission policy before provenance, confidence, expiry, and conflict semantics are designed.

A reference implementation still needs hard capacity bounds and clear conflict behavior. Silent eviction would make Recall depend on hidden cache pressure, while partial batch writes would leave state inconsistent when one admitted Item is invalid.

## Decision

1. Add `memory.Noop`, which returns empty Recall and performs no Observe side effect while honoring Context cancellation.
2. Add a concurrency-safe `memory.InMemory` reference Port with copied initial Items and exact Scope isolation.
3. Recall returns the newest committed matching Items first and applies the configured Recall count, per-Item byte, and total-byte bounds.
4. InMemory defaults to 1,024 retained Items and 4 MiB of retained Item text. Custom capacity remains explicit and positive.
5. Add an injected `memory.Admission` boundary. Observe stores nothing when Admission is absent; Core provides no default extraction or fact decision.
6. Validate every admitted Item, require exact observed Scope, and commit a batch atomically only after conflict and capacity checks. Invalid Admission results remain fatal at the Agent boundary.
7. Reject existing `(Scope, Item ID)` by default, never silently evict, retry Admission, or partially commit. Capacity/conflict returns an explicit error. ADR 0011 adds explicit keep/replace alternatives.
8. Copy requests, initial Items, Admission results, and Recall results. Preserve Context cancellation and fatal panic classification through Agent integration.
9. Make no durability, recovery, audit, cross-process, ordering-across-goroutines, physical deletion, embedding, relevance, or semantic conflict-resolution guarantee. ADR 0011 later adds structural expiry and exact-ID conflict policies.

## Consequences

- Tests and examples can run without a database or network and can exercise real Recall/Observe lifecycle behavior.
- Applications must explicitly choose whether observation becomes an Item; unverified output is not admitted by default.
- Sequential commits have deterministic newest-first Recall. Concurrent commit order is whichever operation obtains the internal lock first.
- Full capacity fails closed rather than changing retained evidence invisibly.
- InMemory cannot substitute for an Enterprise Adapter and loses all state on process termination.
- The Admission interface is a policy hook, not a completed quality policy. ADR 0011 adds structural expiry and exact-ID conflict policies; fact extraction, physical deletion, confidence, and semantic conflict resolution remain future work.

## Rejected Alternatives

- **Automatically store the whole turn:** creates an implicit quality/admission policy and can repeatedly amplify model claims.
- **Unbounded map:** permits tests or long-running local processes to grow memory without limit.
- **LRU eviction:** silently changes Recall and conflates a reference Port with cache policy.
- **Upsert duplicate IDs:** chooses conflict semantics before the quality contract is defined.
- **Use SafeSpace for persistence:** local file confinement does not provide Memory durability, authorization, consistency, or lifecycle semantics.
- **Bundle a vector database:** adds a backend and embedding policy to Core before Enterprise requirements exist.
