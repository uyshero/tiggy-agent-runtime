# ADR 0017: Content-Free Model Context Diagnostic Manifest

- Status: Accepted
- Date: 2026-08-05

## Context

Context Plan, Compression, Memory, Artifact, Retry, and Model Call Events each expose bounded lifecycle evidence, but a consumer cannot directly see the final composition used by one physical Provider call. Recording full `model.Request` would make production Events retain prompts, recalled facts, Tool data, and schemas, while listing every Message ID would make Event size grow with conversation history.

## Decision

1. Add optional `ModelCallPayload.Context` to `model.call.started` within Event Protocol V1.
2. New Runtime calls populate it from the exact prepared Message/Tool view immediately before Provider invocation; old Events may omit it.
3. Use fixed aggregate fields for pre-compression/final Message counts, Roles, Visibility, Runtime projection kinds, Instruction Stack bytes, fixed built-in Instruction Stack kind bytes, Content kinds/bytes, and Tool Definition count/bytes.
4. Identify `instruction_stack`, `memory_recall`, and `context_summary` only from internal System Message Metadata; never retain their bodies, source identifiers, Skill names, or custom kind names.
5. Keep exact compressed source Message IDs in the existing bounded Compression outcome rather than duplicating per-Message manifests on every Model Call.
6. Validate arithmetic relationships in `event.ModelContextPayload.Validate` and through `agenttest.ValidateTrace` when the optional field exists.
7. Treat the payload as reviewed metadata in `record.SafeSanitizer`; expose selected values in structured logs but not as Metric dimensions.
8. Use saturating byte arithmetic and recursively count validated nested Tool Result Content.

## Consequences

- Operators can correlate the final request shape with one Call ID, retry, target, Plan, Compression, Memory Recall, and Artifact lifecycle without retaining content.
- Event payload size remains constant with respect to conversation length.
- Counts reveal workload shape and still require normal diagnostic-data governance.
- The manifest cannot explain semantic relevance or prove factual preservation; controlled Request inspection and Compressor quality evaluation remain separate tools.

## Rejected Alternatives

- **Record the full Model Request:** violates privacy-safe defaults and duplicates sensitive State.
- **Emit one reference per Message:** grows Event size with untrusted previous history and retains identifiers unnecessarily.
- **Add a separate paired operation Event:** duplicates the Model Call lifecycle without an independent operation or failure boundary.
- **Put counts only in logs:** loses portable protocol evidence and prevents Recorder/offline consumers from using it.
- **Turn counts into Metric labels:** creates high-cardinality, low-value series.
