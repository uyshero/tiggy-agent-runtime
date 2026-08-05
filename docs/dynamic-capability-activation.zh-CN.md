# 动态能力激活

## 目标

动态能力激活是未来在任务开始后继续暴露 Tool、MCP 或 Skill 能力的协议。它被刻意推迟到 v0.1.2 静态暴露加固之后，因为运行中改变能力会影响上下文、审批、Retry、Fallback、Recorder 和重放诊断。

目标不是让模型偷偷加工具，而是让每次能力扩展都显式、已授权、可观察、有边界、可诊断重建。

## 能力来源

协议应把这些来源视为同一种 Activation 概念下的不同来源：

| 来源 | 激活可能暴露什么 | 特殊风险 |
|---|---|---|
| Native Toolkit | 选中的 Tool Group | 完整 JSON Schema 会增加上下文和选择空间 |
| MCP | 有界 Catalog Snapshot 中选中的 Server Tool | 重连可能改变 Definition 或可用性 |
| Skill | 选中的指令、Resource 和声明的 Tool Group | 激活同时改变 Prompt 权威和 Tool 集合 |

Activation 不是授权。任何外部副作用前仍必须经过 Permission Policy 和 Approval Policy。

## 建议流程

```text
有界 Catalog 证据
        ↓
Activation Request
        ↓
应用 / Policy / 可选人工审批
        ↓
新的不可变能力 Snapshot
        ↓
下一次 Model Call 接收扩展后的 Definition / Instruction
```

Activation 只在下一次 Model Call 生效。它不能修改已经发出的物理 Model Request，也不能在 Tool Batch 执行中改变 Tool 集合。

## 状态机

```text
stable_snapshot
  ├─ request_activation
  ▼
activation_pending
  ├─ rejected/canceled/failed -> stable_snapshot
  └─ approved
       ▼
pending_next_turn_snapshot
       ▼
stable_snapshot
```

`activation_pending` 可以让一次 `Run` 调用进入 suspended，但 suspended 不是整个任务失败。恢复时必须验证审批通过的 Activation 仍匹配请求中的来源、Group、Definition Digest 和限制。

## 必须定义的契约

动态激活协议必须定义：

- Activation Request ID、来源类型、请求的 Group/Resource/Tool、原因和有界 Catalog 证据；
- 谁授权激活，以及哪些能力允许激活；
- 每次激活的 Group 数、Tool 数、Skill Resource 数、Instruction 字节、Tool Definition 字节和 MCP Catalog 字节上限；
- 激活后的确定性 Snapshot Digest 和每个 Definition Digest；
- 是否允许改变 Prompt，以及它进入 Instruction Stack 的位置；
- Retry、Fallback、Resume 和 Recorder 如何重建同一能力集合；
- 失败、取消、拒绝和 Catalog 过期时的行为；
- 不含正文的 Event 和 Log，用于解释变化，但不保留 Prompt Body、Tool Schema、Backend ID、Skill Resource Body 或 Secret。

## 不变量

1. 一次 Model Call 只能使用一个不可变 Capability Snapshot。
2. Tool Batch 必须使用产生其 Tool Call 的同一个 Snapshot。
3. Approval 必须绑定当前 Snapshot 中的精确 Call 参数和 Tool Definition Digest。
4. Retry 必须重建同一 Active Snapshot，除非 Retry 前已经完成一次显式 Activation Transition。
5. Fallback 从完整 State 开始，但本轮必须重建同一 Active Capability Snapshot。
6. MCP 重连不能静默重放 Tool Call，也不能替换已审批 Batch 使用过的 Tool Definition。
7. Skill Activation 不能夹带隐藏权威：Instruction、选中 Resource、声明 Tool Group 和 Digest 都必须是显式证据。
8. Recorder 可以诊断 Activation 历史，但真正恢复仍需要应用拥有的来源提供匹配 Snapshot。

## 延后实现形态

v0.2.0 可以从保守 Port 开始，而不是直接做完整动态插件系统：

```go
type ActivationPort interface {
    RequestActivation(context.Context, ActivationRequest) (ActivationDecision, error)
}
```

Port 由应用拥有。Core 负责验证、事件顺序、不可变 Snapshot 构造和下一轮生效语义。Enterprise 可以在 Port 后面实现租户策略、数据库状态、人工审批、MCP Fleet Catalog 和审计存储，而不让 Core 依赖这些系统。

## 否决捷径

- 让模型直接启用任意 Tool。
- 把新 Tool Definition 直接追加到当前 Provider Request。
- 在 Tool Batch 执行中修改 Toolkit、MCP 或 Skill 状态。
- 把 MCP 重连当成静默刷新 active definition 的许可。
- 动态注入 Skill Instruction 但不记录 Activation Event。
- 只依赖 Recorder 作为恢复机制。

这些捷径看起来方便，但会让调试和重放变成小型考古现场。
