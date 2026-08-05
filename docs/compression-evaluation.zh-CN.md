# Compressor 质量评测

## 目标

`evaluation/compression` 使用确定性 Fixture 评测应用自己实现的 `contextmanager.Compressor`。它与 Runtime 可靠性评测互补：Runtime 测试证明控制流有界和结构安全；本包检查具体摘要策略是否保留应用声明的事实，并且确实把溢出的 Request 缩减到可放入上下文窗口。

评测器使用与 Agent 相同的公开 `Planner`、`Compressor`、`CompressionRequest` 和 `ApplyCompression` 契约。它不内置摘要模型，也不依赖 Provider、数据库、网络或 Enterprise 服务。

## 在测试中使用

注入应用生产配置使用的 Planner 和 Compressor：

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
    t.Fatal(err) // 评测定义非法。
}
if !report.Passed() {
    t.Fatalf("Compressor quality failed: %#v", report.Summary)
}
```

源 Request 必须按注入的 Planner 判断为 Overflow。每个 Required Fact 都要声明来源 Message ID；只有这些 Message 全部被 Compressor Decision 覆盖，并且结果摘要包含一个允许表达时，该事实才通过。

`AnyOf` 匹配不区分大小写，并折叠空白字符。它是可复现的 Fixture 断言，不是开放世界语义 Judge。应使用稳定、有区分度的短语并列出允许的改写；词面检查通过不代表模型不可能产生幻觉。

## 七项检查

每个 Case 必须同时通过：

1. `decision_valid`：Strategy、Version、正数前缀覆盖和 Summary 合法。
2. `structural_integrity`：Core 接受 Decision 并构造合法压缩视图。
3. `protected_messages_preserved`：所有未覆盖 Message 保持等价且顺序不变。
4. `provenance_exact`：Summary Metadata 精确记录 Strategy 和来源 Message ID。
5. `required_facts_preserved`：声明的来源都已覆盖，摘要命中允许的事实表达。
6. `summary_bounded`：摘要 UTF-8 字节数不超过 Case 上限。
7. `compressed_budget_fits`：重新规划压缩视图后得到合法且 Fits 的 Plan。

报告 Schema 为 `tiggy.compression.quality.v1`。`MarshalJSONReport` 和 `Markdown` 可生成 CI Artifact。报告只包含 Case/Fact ID、Strategy、计数、检查结果、安全 Failure Code 和安全描述；不包含源 Message、Tool 参数/结果、Required Fact 文本、Summary 正文、Delegate 原始错误或 Panic Stack。

## Fixture 矩阵

用多个彼此独立的 Case 评测真实 State 投影下的退化：

- 在短、中、接近窗口上限的历史中放置相同锚点事实；
- 事实同时依赖 Tool Call 与 Tool Result 的工具密集历史；
- 新事实修正旧值的冲突历史；
- 不应挤掉活跃约束的噪声轮次；
- 多语言或领域术语的允许表达；
- 使用生产 Planner 的不同 Model Target 和 Tokenizer。

每个 Case 都从完整源 Message 开始，这与 Runtime 行为一致。普通 Agent 迭代会从完整 `Agent.State` 重新投影，不会递归摘要旧摘要；Provider Context Recovery 也有次数边界，且不会改写完整 State。递归摘要 Benchmark 可用于独立 Summarizer 研究，但不是 Tiggy Runtime 的执行不变量。

## 为什么没有通用 CLI

仓库内的命令无法通过参数构造应用专用的 Go 接口实现。内置 CLI 只能评测 Fake Compressor，或者引入不属于 Core 的 Provider/配置耦合。应用应在自己的测试中调用评测库，并把 JSON/Markdown 写入 CI Artifact 保存位置。

本评测不是延迟 Benchmark、人工偏好测试、幻觉检测器或 Model-as-Judge 框架。这些能力可以在单独治理的评测层复用同一批 Fixture。
