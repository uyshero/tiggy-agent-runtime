# Artifact Offload 语义

## 范围

`artifact` 通过调用方注入的 Storage，把大型成功 Tool Result 排除在模型 Context 之外。它定义可移植 Payload、有完整性绑定的 opaque Reference、有界默认 Offloader 和 Agent Lifecycle Event，不提供数据库或 Object Store 实现。

```go
offloader, err := artifact.NewBoundedOffloader(artifact.BoundedOffloaderOptions{
    Store: storeAdapter,
    MaxInlineBytes: 64 << 10,
    MaxArtifactBytes: 16 << 20,
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    Name: "assistant",
    Model: provider,
    ModelIdentity: identity,
    ArtifactOffloader: offloader,
})
```

阈值作用于编码后的 Envelope，不只是可见文本。零值使用上面的默认值；自定义限制必须满足 `0 < MaxInlineBytes < MaxArtifactBytes`。Offload 是 Tool 执行后的投影边界，不能替代每个 Tool/MCP Adapter 对自身输出内存分配的限制。

## Payload 和 Reference

`artifact.NewRequest` 把完整且已校验的 `message.ToolResult`（包括 Content、State、Call ID 和 Tool Name）序列化为 `tiggy.artifact.tool-result.v1`。授权 Retrieval Adapter 可通过 `DecodeToolResult` 严格解码同一 Envelope；未知字段、任意嵌套层级的重复对象键和尾随值都会被拒绝。

每个 Request 包含精确 Byte、Media Type `application/vnd.tiggy.tool-result+json;version=1` 和 `sha256:<hex>` Digest。Offloader 可保持 Inline 或返回 Reference。只有 Reference 的 Media Type、Byte Size 和 Digest 与 Request 匹配时，Core 才接受 Offloaded Decision。

Offload 后模型可见 Result 只包含一个 `message.ContentArtifact`。`ObjectRefID` 是 opaque ID，不是 URL。Signed URL、Query、Fragment、Credential 和后端 Record 都禁止进入协议。State 只保留 Offload Metadata 和 Reference；原始 Result 可从 Store 中的编码 Payload 恢复。

## Store Contract

`artifact.Store.Put` 接收独立 Byte Copy、Tool Identity、Media Type、Digest 和建议性 Idempotency Key，只返回 opaque Object ID。Runtime 只调用一次，不自动重试或删除：

- Timeout/Cancellation 可能发生在 Storage 已接受对象之后；
- Retry 可能重复外部副作用；
- 后续 Run 失败时 Delete 可能删除已被其他 Consumer 看到的对象。

Store Adapter 定义 Durability、Scope/Authorization、Encryption、Retention、Retrieval、Deduplication 和 Orphan Cleanup。只要保留的 Agent State 仍可能引用对象，Object ID 就必须可解析。SafeSpace 不是隐式 Artifact Store。

Core 不自动把 Artifact Content 取回 Context。模型需要检查完整值时，应用必须注册显式授权的 Retrieval Tool，并通过自己的 Policy 解析 Object ID。

## Agent 行为

Offload 在成功 Tool Execution 后、Result 投影前运行。模型可处理 Tool Error 保持 Inline。未配置 Offloader 时完全保留原有行为。

```text
tool.result.offload.started
tool.result.offload.completed: inline | offloaded
tool.result.offload.failed: failed | canceled
```

Event 携带 Call/Tool Identity、编码 Byte Size、Media Type、Digest、Object ID、Status 和 Fallback，绝不携带 Result Byte。Inline/Offloaded 使用 `fallback=none`，所有失败使用 `fail_closed`。

Store/Offloader error 和 panic 返回 `artifact/artifact_offload_failed`，非法 Decision 返回 `artifact/invalid_artifact_offload`，Size Overflow 返回 `limit/artifact_payload_limit`，取消返回 `canceled/artifact_offload_canceled`。Agent 写入安全 Error Tool Result、关闭 Tool Call，并在下一次 Model Call 前终止。

## 所有权与非目标

Request、Put Buffer、Decision、Projected Content 和 Decoded Result 在边界处复制。`BoundedOffloader` 不可变；Store 并发安全时它也并发安全。被并发复用 Agent 使用的自定义 Offloader 必须并发安全并响应 Context。

Core 不选择 Cloud SDK、Bucket、Database、Tenant Scope、Encryption Key、Retention Policy、Signed URL Mechanism、Retrieval API 或 Garbage Collector。这些属于 Adapter 或 Enterprise。
