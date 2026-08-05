# ADR 0007: Structurally Safe Context Compression

- Status: Accepted
- Date: 2026-08-03

## Context

Token planning can measure an exact input deficit, but removing arbitrary Messages can separate Tool Calls from Results, discard current work, mutate application-owned history, or hide what was summarized. Core cannot choose a universal summarization model or prove the factual quality of generated prose. It can enforce structural invariants around an injected policy.

## Decision

1. Compression is optional, model-specific, and requires a Context Planner.
2. It runs after a validated overflow Plan, or through the explicit bounded Provider context-length recovery in ADR 0015, and receives owned copies through `contextmanager.Compressor`.
3. Core groups User turns in order and merges boundaries crossed by Tool Call/Result relationships.
4. System groups, the latest turn, and groups containing unresolved Tool Calls are protected. Only a contiguous inactive prefix is eligible.
5. A `CompressionDecision` selects a positive number of eligible groups and returns summary text plus a required strategy name/version. It cannot construct the final Message list.
6. Core replaces the covered groups with one internal System Message, records source Message IDs in Metadata, and leaves full Agent State unchanged.
7. The compressed view is planned again exactly once. It reaches the provider only when the second Plan fits.
8. Strategy/version, source coverage, before/after tokens, summary ID, and fallback are emitted without prompt or summary bodies.
9. Errors, panics, invalid Decisions, cancellation, no eligible history, and insufficient reduction fail closed. Core does not silently truncate a tail or recursively compress.
10. Ordinary same-target retries reuse the unchanged compressed view. One policy-authorized Provider context-length recovery may compress that failed view once; a Fallback target starts from full State and applies its own Planner and optional Compressor.

## Consequences

- Core can test coverage, ordering, active-work retention, Tool relationships, and bounded behavior deterministically.
- Applications can use a local algorithm, a separately governed summarizer, or no Compressor.
- “Semantics-preserving” means structurally safe; factual summary fidelity remains the injected implementation's responsibility.
- A Compressor that performs external model calls currently owns their usage, cost, retry, and resource accounting. Those calls are not added to Agent `Usage` or `MaxModelCalls` by this contract.
- Compression reduces only the provider request view, so subsequent iterations can reproject from complete State rather than recursively summarizing summaries.

## Rejected Alternatives

- **Character or token tail truncation:** simple but can break protocol relationships and silently discard active work.
- **Let the Compressor return arbitrary Messages:** makes ordering and protected-history guarantees unverifiable.
- **Rewrite Agent State with the summary:** loses source history and weakens application-managed continuation.
- **Retry compression until it fits:** creates an implicit unbounded loop and compounds summary loss.
- **Use Provider context-length errors instead of preflight:** wastes calls and loses the measured preflight guarantee. ADR 0015 retains preflight and permits only one explicit recovery for residual mismatch.
