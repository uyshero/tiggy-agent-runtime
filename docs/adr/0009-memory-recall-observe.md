# ADR 0009: Scoped Memory Recall and Observe

- Status: Accepted
- Date: 2026-08-03

## Context

An Agent may benefit from information retained outside its current State, while an application may want to inspect a completed turn for later storage. Putting database or vector-store records in Core would couple the runtime to Enterprise identity, durability, indexing, and retention choices. Injecting recalled text directly into State would also make transient retrieval results look like conversation history and persist them through `Continue`.

Memory failure has different semantics from context planning. Missing optional recall often permits useful execution, but accepting crossed-scope or malformed results would violate isolation and make model input untrustworthy. Observation happens after model completion, so its failure must not erase the already committed response by default.

## Decision

1. Add a standard-library-only `memory.Port` with `Recall` and `Observe` operations and Tiggy-owned Request/Result values.
2. Require an application-defined `Scope{Namespace, Key}` for a configured Port. Every recalled Item must exactly match it.
3. Require each Item to carry a unique ID, non-empty UTF-8 text, and `Provenance{Source}`.
4. Invoke Recall once per Run after input validation and before model execution.
5. Project recalled Items as one internal System Message labeled as contextual evidence. Insert it after leading System Messages only in the model request view; never append it to Agent State.
6. Invoke Observe once after successful final-response commit and Model Call completion. Pass only messages created in the current turn, excluding System and recalled messages.
7. Bound Recall to 16 Items, 8 KiB per Item, and 64 KiB total text by default. Bound encoded Observation to 256 KiB.
8. Default ordinary Recall/Observe errors to `degrade`; allow each operation to select `fail_run`.
9. Always treat cancellation as Run cancellation. Treat Adapter panic, invalid/cross-scope result, duplicate Item ID, and result-limit violation as fatal contract failures.
10. Copy values across the Port boundary, contain panic, never retry automatically, and emit paired events without Memory text, observed messages, or Scope key.
11. Provide no Store, database, vector client, Noop/in-memory implementation, fact-admission policy, TTL, deletion, confidence, or conflict semantics in this stage.

## Consequences

- Applications can add durable, vector, relational, or remote Memory without changing Agent runtime contracts.
- Recall participates in Context planning because it enters the provider request before planning, while full State remains stable and portable.
- Default degradation preserves availability and produces explicit diagnostic evidence.
- Scope mismatch and malformed result cannot silently degrade, reducing cross-scope leakage risk.
- Observe receives model output that is already committed. `fail_run` therefore reports a failed/canceled Run with the completed response still present in State but no `Final` success value.
- Enterprise adapters remain responsible for authorization, durability, consistency, encryption, indexing, retention, deletion, backup, and fact quality.
- The Event protocol defines five additive Memory lifecycle types with one non-sensitive payload schema.

## Rejected Alternatives

- **Store Memory in Agent State:** conflates retrieved evidence with conversation truth and causes transient recall to persist through `Continue`.
- **Expose database/vector records:** leaks backend and Enterprise identity types into the embeddable Core API.
- **Always fail the Run:** turns an optional quality feature into a hard availability dependency.
- **Always ignore failures:** hides observability gaps and would permit scope/contract violations.
- **Observe the full State:** repeatedly reprocesses prior turns and exposes System instructions and recalled projection to storage policy.
- **Automatically retry:** storage/retrieval costs and side effects are Adapter-specific and cancellation can make completion ambiguous.
