# Context Diagnostics

## Purpose

Every new Runtime `model.call.started` payload includes an optional `context` manifest describing the exact Provider Request view for that physical Model Call. The manifest is fixed-size and content-free. It connects context planning, Memory projection, Compression, Artifact references, Tool definitions, retries, and Fallbacks to the Call ID and attempt that actually used them.

The field is additive to Event Protocol V1. Older recordings may omit it; `agenttest.ValidateTrace`, observers, and readers remain compatible with those Events.

## Read A Snapshot

```go
for _, value := range result.Events {
    if value.Type != event.ModelCallStarted {
        continue
    }
    payload, err := agenttest.DecodePayload[event.ModelCallPayload](value)
    if err != nil || payload.Context == nil {
        continue
    }
    snapshot := payload.Context
    log.Printf("call=%s projected=%d request=%d instruction_stack=%d instruction_stack_bytes=%d summaries=%d memory=%d",
        payload.CallID,
        snapshot.ProjectedMessageCount,
        snapshot.RequestMessageCount,
        snapshot.InstructionStackMessages,
        snapshot.InstructionStackBytes,
        snapshot.CompressionSummaries,
        snapshot.RecalledMemoryMessages,
    )
}
```

`ProjectedMessageCount` is the complete per-Run model view entering the selected Model Target before that target's compression. `RequestMessageCount` is the final Message count for this physical Provider call. A retry has a new Call ID and its own manifest. A Fallback starts from the full projection and uses its own Planner/Compressor. A Provider context-length recovery therefore shows the reduced retry view directly.

The manifest contains:

- Message counts by Role and Visibility;
- Instruction Stack Message count, aggregate text bytes, and fixed per-kind static instruction bytes;
- recalled-Memory projection and Compression-summary Message counts;
- Text/Thinking/Image/Tool Call/Tool Result/Artifact block counts;
- aggregate UTF-8 Text, Thinking/signature, and Tool argument bytes;
- Tool Definition count and aggregate name/description/schema/class bytes;
- the message count before target compression and in the final Request.

These byte totals are diagnostics, not Token estimates. Use `context.prepared` for tokenizer-aware input/window/overflow arithmetic.

## Reconstruct A Decision

Use one Call ID/attempt as the anchor and read the related evidence in order:

1. `memory.recall.*` explains whether Recall completed, degraded, or filtered expired Items and reports bounded item/byte counts.
2. `context.prepare.*` records the Planner source, window, reserve, margin, input, remaining/overflow, and pressure.
3. `context.compression.*` records why compression ran, the strategy, exact covered Message IDs, before/after Tokens, and whether the result fit.
4. `model.call.started.context` records the final content-free Request composition used by that Call.
5. `tool.result.offload.*` explains whether a large Tool Result remained inline or became an Artifact Reference before later projections.
6. `retry.scheduled` and `fallback.activated` explain why the next physical attempt exists.

The snapshot does not claim that a semantic fact was selected or omitted correctly. Exact Request bodies are available only through controlled `agenttest.ScriptedModel.Calls()` or a test Model assertion; they must not be copied into production logs. Compressor fact quality is evaluated separately through `evaluation/compression`.

## Privacy And Bounds

The manifest never contains Message IDs, Prompt/Summary/Memory text, Tool names, Tool arguments/results, Tool schema bodies, Artifact object IDs, Memory Item IDs, Scope keys, or Parameters. It records only that an Instruction Stack message exists, its aggregate text byte size, and fixed built-in kind byte totals, not the static instruction bodies, segment sources, Skill names, or custom kind names. Its field count is constant regardless of conversation length. Byte totals use saturating arithmetic.

`record.SafeSanitizer` retains the reviewed manifest. `observe.LogSink` writes only selected aggregate fields at the existing Model Call log level. Metrics do not use these values as dimensions, avoiding high-cardinality series.

Applications still own log and Journal authorization, encryption, retention, and deletion. Counts can reveal workload shape even when bodies are absent.

Protocol V1 golden fixtures cover both a new `model.call.started` Event with the complete Manifest and a legacy Event without the optional field. The round-trip test validates the typed payload in addition to the generic Event envelope.
