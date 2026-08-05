# Context 诊断快照

## 目标

新版本 Runtime 的每个 `model.call.started` Payload 都会包含可选 `context` Manifest，用固定大小且不含正文的统计描述该次物理 Model Call 最终使用的 Provider Request View。它把 Context Planning、Memory Projection、Compression、Artifact Reference、Tool Definition、Retry 和 Fallback 与真正使用它们的 Call ID/Attempt 关联起来。

该字段是 Event Protocol V1 的向后兼容增量。旧 Recording 可以缺失它；`agenttest.ValidateTrace`、Observer 和 Reader 仍接受旧 Event。

## 读取快照

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

`ProjectedMessageCount` 是选定 Model Target 执行自身压缩前的完整本 Run 模型视图；`RequestMessageCount` 是这次物理 Provider Call 最终使用的 Message 数。每次 Retry 有新 Call ID 和独立 Manifest。Fallback 从完整投影重新开始，并使用自己的 Planner/Compressor。因此 Provider Context Length 恢复会直接体现压缩后的重试视图。

Manifest 包含：

- 按 Role 和 Visibility 分类的 Message 数；
- Instruction Stack Message 数、聚合文本字节数，以及固定内置类型的静态指令字节数；
- Recall Memory Projection 与 Compression Summary Message 数；
- Text、Thinking、Image、Tool Call、Tool Result、Artifact Block 数；
- Text、Thinking/Signature 和 Tool Argument 的 UTF-8 聚合字节数；
- Tool Definition 数量以及 Name/Description/Schema/Class 聚合字节数；
- Target 压缩前和最终 Request 中的 Message 数。

这些字节统计用于诊断，不是 Token 估算。Tokenizer-aware 的 Input/Window/Overflow 算术应读取 `context.prepared`。

## 还原一次决策

以一个 Call ID/Attempt 为锚点，按顺序查看：

1. `memory.recall.*`：Recall 是 Completed、Degraded，还是过滤了 Expired Item，并提供有界 Item/Byte 计数。
2. `context.prepare.*`：Planner Source、Window、Reserve、Margin、Input、Remaining/Overflow 和 Pressure。
3. `context.compression.*`：压缩触发原因、Strategy、精确覆盖 Message ID、前后 Token 以及最终是否 Fits。
4. `model.call.started.context`：该 Call 最终 Request 的无正文组成。
5. `tool.result.offload.*`：大型 Tool Result 在后续投影前保持 Inline，还是转换为 Artifact Reference。
6. `retry.scheduled` / `fallback.activated`：下一次物理尝试为什么存在。

快照不能判断某个语义事实被保留或遗漏是否正确。完整 Request 正文只应通过受控的 `agenttest.ScriptedModel.Calls()` 或测试 Model Assertion 查看，禁止复制到生产日志。Compressor 事实质量由 `evaluation/compression` 单独评测。

## 隐私与边界

Manifest 不包含 Message ID、Prompt/Summary/Memory 正文、Tool Name、Tool 参数/结果、Tool Schema Body、Artifact Object ID、Memory Item ID、Scope Key 或 Parameters。它只记录是否存在 Instruction Stack Message、聚合文本字节大小，以及固定内置类型的字节总量；不记录静态指令正文、Segment Source、Skill 名称或自定义 Kind 名称。字段数量不会随对话长度增长；字节计数使用饱和算术。

`record.SafeSanitizer` 保留已审查的 Manifest。`observe.LogSink` 只在现有 Model Call 日志级别写入选定聚合字段。Metrics 不把这些数值作为维度，避免产生高基数时间序列。

即使不含正文，计数仍可能反映工作负载形态；应用继续负责日志与 Journal 的授权、加密、保留和删除。

Protocol V1 Golden Fixture 同时覆盖携带完整 Manifest 的新 `model.call.started` Event，以及缺少可选字段的旧 Event。Round-trip Test 除了通用 Event Envelope，还会校验强类型 Payload。
