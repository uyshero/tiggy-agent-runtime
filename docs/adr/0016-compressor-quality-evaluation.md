# ADR 0016: Deterministic Compressor Quality Evaluation

- Status: Accepted
- Date: 2026-08-05

## Context

ADR 0007 lets Core prove structural compression invariants but deliberately leaves factual summary quality to the injected Compressor. Unit tests that only validate `ApplyCompression` can therefore pass even when a real summary drops an application-critical fact. A universal semantic judge would add model, cost, privacy, and nondeterminism concerns to Core.

## Decision

1. Add `evaluation/compression` as an opt-in test library, not a Runtime execution dependency.
2. The application injects the same Planner and Compressor used in production and supplies complete overflowing Request fixtures.
3. Every required fact identifies its source Message IDs and one or more deterministic accepted textual representations.
4. A fact passes only when all declared sources are covered and the generated summary matches an accepted representation after case-folding and whitespace normalization.
5. Core applies the Decision and the evaluator independently checks protected Message preservation, exact provenance, a per-Case summary byte bound, and a fitting post-compression Plan.
6. Invalid suite definitions fail before delegate calls. Planner/Compressor errors and panics become failed Cases with stable safe descriptions.
7. Reports omit source/summary bodies, fact text, arbitrary delegate errors, and Panic stacks.
8. Fixtures model repeated quality pressure as independent full-State projections at increasing history sizes. The evaluator does not invent recursive summary behavior that Runtime does not perform.

## Consequences

- Compressor implementations can carry an application-specific, deterministic CI gate without changing Core execution semantics.
- Tool-derived facts have auditable Call/Result source coverage rather than an untraceable expected string.
- Lexical accepted variants are reproducible but cannot prove general semantic equivalence or absence of hallucinations.
- A separately governed human or model Judge may consume the same fixtures later; it must not weaken this deterministic baseline or become an implicit Runtime dependency.
- There is no generic CLI because Core cannot instantiate an application's Go Compressor implementation from command-line configuration.

## Rejected Alternatives

- **Declare structure tests sufficient:** they cannot detect a fluent but factually incomplete summary.
- **Bundle one summarization model:** couples Core to a Provider, credentials, cost, and model behavior.
- **Use model-as-judge as the Core gate:** introduces nondeterminism and circular Provider dependence.
- **Recursively summarize outputs by default:** evaluates behavior the Runtime intentionally avoids and compounds information loss.
- **Write prompt and summary bodies into reports:** improves ad hoc debugging at the cost of unsafe default data retention.
