# Context Manager 语义

## Token Budget Planner

`contextmanager.TokenPlanner` 在 Model Call 之前进行不可变预检。它通过注入的 `model.TokenCounter` 计算完整 Provider Request，预留输出和 Safety Margin，返回可校验 Plan。Planning 不删除、摘要、重排或修改 Message/Tool。

```go
planner, err := contextmanager.NewTokenPlanner(contextmanager.TokenPlannerOptions{
    Counter: providerCounter,
    CountSource: contextmanager.CountSourceProvider,
    ContextWindowTokens: 128_000,
    SafetyMarginTokens: 1_024,
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    Name: "assistant",
    Model: provider,
    ModelIdentity: identity,
    ContextPlanner: planner,
})
```

Counter 看到 Model Identity、完整 Message List 和 Tool Definition 的独立副本，因此 System/Skill 指令、会话历史、Tool Call/Result、Tool Schema 和 Recall Memory Projection 都被统计。

Core 不安装“字符数除以四”的隐式估算器。应用必须注入 Provider-aware Counter，或显式标记为 `estimated` 的 Counter。`provider` 表示应用认为该 Counter 了解 Provider Tokenizer；Core 不独立认证其完全一致。

## Budget 不变量

```text
available_input = context_window - output_reserve - safety_margin
required        = input + output_reserve + safety_margin
remaining       = max(available_input - input, 0)
overflow        = max(input - available_input, 0)
utilization     = input / available_input，单位 Basis Point
```

所有计数均为正且有界值。`Plan.Validate` 校验上述算式、Model Identity、Count Source、阈值、Fits 和 Pressure。应用可通过小型 `Planner` 接口注入自定义实现，但 Agent 会再次校验 Plan，并拒绝修改 Model Identity 或 Output Reserve。

默认压力阈值为 80% Warning 和 95% Critical。Pressure 只用于决策与观测，不会自动删除 Context。任意正数 Overflow 都返回 `PressureOverflow` 和 `Fits=false`。

Agent 使用 `Limits.MaxOutputTokensPerCall` 作为 Output Reserve。一个 Generation Cycle 内，每个 Model Target 只规划一次：同 Target Retry 复用未变 Plan，Fallback 使用自己的 Planner/Window，下一个 ReAct 迭代因 Tool Result 改变上下文而重新规划。启用 Planning 时，每个 Fallback 都必须配置自己的 Planner。

## Runtime 行为

```text
context.prepare.started
context.prepared: ready | overflow | failed | canceled
```

Completed Event 记录 Model、Count Source、Window、Input、Output Reserve、Safety Margin、Available/Remaining/Overflow、Utilization、Pressure 和 Status，不记录 Prompt 或 Tool Schema 正文。

未配置 Compressor 时，Overflow 会在 `model.call.started` 前以 `limit/context_overflow` 终止。配置后，`context.prepared` 中的精确 `OverflowTokens` 成为 Compressor 的量化缩减目标。Counter/Planner 失败为 `internal/context_planner_failed`，非法自定义 Plan 为 `internal/invalid_context_plan`，取消为 `canceled/context_prepare_canceled`。Counter 和自定义 Planner panic 都会被隔离。

## 结构安全 Compression

每个 Model 可在 Context Planner 旁绑定 `contextmanager.Compressor`。只有已校验 Plan 发生 Overflow 后才运行 Compression。Core 构造有序的非活跃历史组，并把独立副本交给 Compressor：

```go
type Compressor interface {
    Compress(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error)
}
```

Decision 提供 Strategy Name/Version、从第一个 Eligible Group 开始覆盖的正数组数，以及摘要文本，不提供最终 Message List。Core 通过 `ApplyCompression` 用一条 Internal System Summary 替换这些组，并在 Metadata 中写入来源 Message ID。因此 Compressor 不能重排受保护消息，也不能拆分语义组。

分组与保护规则是确定性的：

- 一个 User Turn 及其后续 Assistant/Tool Message 构成有序组；
- Tool Call/Result 跨越的边界会被合并；
- 开头 System Message、后续 System Group、当前轮和包含未完成 Tool Call 的组受保护；
- 只有连续的非活跃前缀可压缩；
- 完整 `Agent.State` 永不改写，只有下一次 Provider Request 使用摘要视图。

一次压缩后，Agent 必须重新规划。结果 Fit 才进入 `model.call.started`；压缩不足返回 `limit/context_overflow`，不调用 Provider。Compressor error/panic 返回 `internal/context_compression_failed`，非法 Decision 返回 `internal/invalid_compression_result`，取消返回 `canceled/context_compression_canceled`。没有 Eligible Group 时返回 `limit/context_compression_unavailable`。

```text
context.compression.started
context.compression.completed: completed | insufficient
context.compression.failed: failed | canceled
```

Outcome 记录 Model、压缩前/后/目标 Input Token、Overflow、Strategy、覆盖 Group 数、来源 Message ID、Summary Message ID 和 Fallback。内置回退只有 `fail_closed`；Core 不提供有损 Tail Truncation 回退。

这里的“语义保持”是结构契约。Core 能证明覆盖范围、顺序、Tool 关系和活跃工作被保留，但无法证明任意摘要文本在事实上完全忠实。Compressor 由调用方拥有，必须响应 Context；同一 Agent 并发复用时还必须并发安全。

`ContextPlanner=nil` 保留 P3 之前的行为，直接发送请求而不做 Token 预检。可靠生产配置应显式提供 Planner；Core 无法仅从 Model Name 准确推断 Window 和 Tokenizer。

## 所有权与非目标

Planner Request/Result 都是复制值。`TokenPlanner` 不可变；注入 Counter 并发安全时，Planner 也并发安全。Context 取消会传播，但 Core 无法强制抢占忽略 Context 的 Counter。

本阶段不提供 Model-backed Summarizer、Provider Context-length Retry、分类 Token 归因或 Prompt Cache 决策。调用外部模型的 Compressor 自己拥有该调用和资源/成本报告；当前 Core 契约不会把它计入 Agent `Usage` 或 `MaxModelCalls`。

大型 Tool Result Artifact Offload 已作为独立投影前边界实现，详见 [Artifact Offload 语义](./artifact.zh-CN.md)。
