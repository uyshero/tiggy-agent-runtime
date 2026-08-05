# Compressor Quality Evaluation

## Purpose

`evaluation/compression` evaluates the application-owned `contextmanager.Compressor` against deterministic fixtures. It complements Runtime reliability tests: Runtime tests prove bounded control flow and structural safety, while this package checks whether a particular summary strategy retains application-declared facts and actually reduces an overflowing request enough to fit.

The evaluator uses the same public `Planner`, `Compressor`, `CompressionRequest`, and `ApplyCompression` contracts as Agent. It does not install a summarization model or depend on a Provider, database, network, or Enterprise service.

## Use It In Tests

Inject the same Planner and Compressor used by the application:

```go
report, err := compressioneval.Evaluate(ctx, compressioneval.Config{
    Planner:    planner,
    Compressor: compressor,
}, []compressioneval.Case{{
    ID: "tool-result-retention",
    Request: contextmanager.Request{
        Model:               identity,
        Messages:            fixtureMessages,
        Tools:               fixtureTools,
        OutputReserveTokens: 4_096,
    },
    RequiredFacts: []compressioneval.RequiredFact{
        {
            ID:               "account-owner",
            SourceMessageIDs: []string{"user_older"},
            AnyOf:             []string{"owner is Ada", "owner: Ada"},
        },
        {
            ID:               "lookup-value",
            SourceMessageIDs: []string{"assistant_call", "tool_result"},
            AnyOf:             []string{"lookup returned 42"},
        },
    },
    MaxSummaryBytes: 8 << 10,
}})
if err != nil {
    t.Fatal(err) // Invalid evaluation definition.
}
if !report.Passed() {
    t.Fatalf("Compressor quality failed: %#v", report.Summary)
}
```

The source Request must overflow according to the injected Planner. Every required fact names its source Message IDs. A fact passes only when all those Messages are covered by the Compressor's Decision and the resulting summary contains one allowed representation.

`AnyOf` matching is case-insensitive and collapses whitespace. It is intentionally a deterministic fixture assertion, not an open-world semantic judge. Include stable, discriminating phrases and accepted paraphrases; do not treat a passing lexical check as proof that a model cannot hallucinate.

## Checks

Each Case must pass all seven checks:

1. `decision_valid`: the strategy, version, positive prefix coverage, and summary are valid.
2. `structural_integrity`: Core accepts the Decision and constructs a valid compressed view.
3. `protected_messages_preserved`: every uncovered source Message remains byte-for-byte equivalent and ordered.
4. `provenance_exact`: summary Metadata identifies the exact strategy and covered Message IDs.
5. `required_facts_preserved`: every declared source is covered and an allowed fact representation appears in the summary.
6. `summary_bounded`: summary UTF-8 bytes do not exceed the Case limit.
7. `compressed_budget_fits`: replanning the compressed view produces a valid fitting Plan.

The versioned report schema is `tiggy.compression.quality.v1`. `MarshalJSONReport` and `Markdown` produce CI-friendly artifacts. Reports contain Case/fact IDs, strategy identity, counts, booleans, safe failure codes, and safe descriptions. They do not contain source Messages, Tool arguments/results, required fact text, summary text, arbitrary delegate errors, or Panic stacks.

## Fixture Matrix

Use multiple independent Cases to measure degradation across realistic State projections:

- short, medium, and near-window histories containing the same anchor facts;
- Tool-heavy histories where facts depend on both Tool Call and Tool Result Messages;
- corrections where a newer fact supersedes an older value;
- noisy turns that must not displace active constraints;
- multilingual or domain-specific accepted representations;
- different Model targets and tokenizers using their production Planners.

Each Case starts from full source Messages, matching Runtime behavior. Normal Agent iterations reproject from full `Agent.State`; they do not recursively summarize prior summaries. Provider context recovery is also bounded and never rewrites full State. A recursive-summary benchmark may still be useful for a standalone summarizer, but it is not a Tiggy Runtime execution invariant.

## Why There Is No Generic CLI

A command in this repository cannot construct an application-specific Go interface implementation from flags. A bundled CLI would therefore evaluate only a fake Compressor or require Provider/configuration coupling that does not belong in Core. Applications should call the library from their own tests and write the returned JSON/Markdown wherever their CI retains artifacts.

This evaluation is not a latency benchmark, human preference study, hallucination detector, or model-as-judge framework. Those can consume the same fixtures in a separately governed evaluation layer.
