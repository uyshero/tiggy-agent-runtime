# 渐进式能力暴露改进方案

## 问题

Tool Definition 会进入每次 Provider Request。Toolkit 较大时，模型还没有开始对话，上下文窗口就可能已经被 Schema 占用；无关能力过多还会增加模型选错 Tool 的概率和误调用的影响范围。

Runtime 已经具备保守实现所需的基础：Toolkit Group、每 Run 不可变 Snapshot、Skill Discovery/Activation、MCP Catalog 边界、Permission Policy、Approval Suspension，以及无正文 Model Context Manifest。本方案组合这些能力，不改变 Tool 执行语义。

## 原则

只暴露当前任务真正需要的最小能力，并让每次扩展都显式、可观察：

```text
有界发现 -> 显式选择 -> 不可变 Snapshot -> 执行 Policy -> Tool Call
```

渐进式暴露控制的是上下文和选择空间，不是授权、审批或 Tool 安全性证明。Permission 和 Approval 仍然是独立且必需的边界。

## 暴露层级

### Level 0：不暴露 Tool

纯对话、规划或不可信输入分流时使用。Model Request 不包含 Tool Definition。

### Level 1：有界 Catalog

只暴露由应用拥有的有限 Metadata：能力 Group、短摘要、可用性和可选 Risk Class。不暴露完整 JSON Schema、Credential、Backend ID 或隐藏 Tool 参数。Catalog 必须有明确的条目数和字节上限。

### Level 2：Group 激活

应用选择一个或多个可信 Group，并创建 Toolkit Snapshot。模型只接收该 Snapshot 的完整 Definition。Input Schema 必须与 Toolkit 编译的 Definition 保持字节一致。

### Level 3：执行

Runtime 校验 Tool Call，在执行前检查 Permission，并对整个 Tool Batch 执行 Approval Policy。暴露过程不能绕过这些检查。

## 与现有 Core 的映射

| 需求 | 现有 Core 边界 |
|---|---|
| 选择能力子集 | `agent.Config.ToolGroups` 与 `Toolkit.SnapshotGroups` |
| 保持集合稳定 | Run 创建时生成的不可变 Toolkit View |
| 限制发现能力 | MCP Page/Tool/Catalog 限制与 Skill Catalog 限制 |
| 统计 Schema 成本 | 注入的 `model.TokenCounter` 与 `model.call.started.context` 聚合字段 |
| 阻止未授权效果 | Permission Policy 与 Approval Policy |
| 解释实际使用能力 | `run.started` Provenance、Tool 生命周期 Event 和 Context Manifest |

首版应直接使用这些 API。未配置 Group 的旧调用保持原行为；在兼容性审查前，渐进式暴露保持 opt-in，不改变默认“暴露全部已注册 Tool”的行为。

## 交付计划

### v0.1.2：静态暴露加固

- 将 `ToolGroups` 文档化为当前支持的渐进式暴露机制。
- 增加零 Group、单 Group、多 Group 投影的确定性测试，并验证 `model.call.started.context` 中的 Schema 字节统计。
- 增加外部使用示例：Run 前选择 Group，并在 `agenttest.ModelStep.Assert` 中断言准确 Tool 集合。
- 增加发布说明和迁移指导。本补丁版本不改变默认“全部已注册 Tool”行为。
- 将有界 Catalog 摘要 Helper 延后到动态激活设计中，与 Catalog 证据、激活授权、Catalog 过期行为和来源级限制一起定义。

### v0.2.0 或以后：显式动态激活

静态契约验证完成后，才考虑动态 Activation Port。它必须明确：

- Tool、MCP 和 Skill 是同一种 Activation 概念下的不同来源；
- 不含完整 Schema 或 Backend ID 的有界 Catalog 摘要；
- 谁授权激活，以及哪些 Group、Server Tool、Skill 或 Resource 可以激活；
- 每次激活的 Group 数、Tool 数、Skill Resource 数、Instruction 字节、Tool Definition 字节和 MCP Catalog 字节上限；
- 只通过新的不可变 Snapshot 在下一轮生效；
- 用于 Approval 和 Recording 的确定性 Snapshot/Definition Digest；
- Cancellation、Rejection、Failure、Catalog 过期和 Suspension 语义；
- Retry/Fallback 如何重建同一 Active Set。

Active Tool Set 绝不能在 Tool Batch 执行中或物理 Model Call 中途变更。动态激活请求必须作为显式状态转换，并生成新的不可变 Snapshot。

v0.2.0 设计草案见 [动态能力激活](./dynamic-capability-activation.zh-CN.md) 和 [ADR 0019](./adr/0019-dynamic-capability-activation.md)。

## 验收标准

1. 不传 Group 调用 `Toolkit.SnapshotGroups()` 会生成空 View。
2. Agent 省略 `Config.ToolGroups` 时保持兼容默认值，仍发送全部已注册 Tool Definition。
3. 选择 Group 的 Run 只发送这些 Group 的并集，顺序稳定且 Schema 不变。
4. Context Planning 统计选中的 Definition；完整 Request 超限时 Fail-closed。
5. Approval Digest 与 Recorder Evidence 绑定到该 Tool Batch 使用的确切 Snapshot。
6. Retry 和 Fallback 不能静默增加 Tool。
7. 测试证明 Run 开始后修改源 Toolkit 不会改变 Active View。
8. Log 和 Metrics 只报告有界聚合计数，不包含 Tool 参数、Schema 正文、Scope Key 或 Credential。

## 否决方案

- 发送前截断或摘要 JSON Schema：会改变可执行契约。
- 根据模型意图自动启用 Tool：隐藏能力/权限转换，难以审计。
- Batch 执行中修改 Toolkit：会使 Approval Digest 和 Replay Diagnosis 失效。
- 把 MCP 重连当成隐式 Activation Refresh：会在没有状态转换的情况下改变 Active Tool Definition。
- 动态注入 Skill Instruction 但没有 Provenance：会在缺少审计证据的情况下改变 Prompt 权威。
- 在 Bootstrap Prompt 中暴露全部 Tool 名称和完整 Schema：仍保留上下文和选择问题。
- 把暴露过程当作安全边界：可见能力不等于授权。

## 运行建议

应用应在产品边界根据用户意图、Tenant Policy 和可信 Workflow State 选择 Group。应通过有权限的 Audit Channel 记录选中的 Group 和 Snapshot Digest，同时使用 Runtime Manifest 做无正文 Request 形态诊断。大型参考数据应放入有界 Tool Result 或 Artifact，而不是 Tool Description 或 Schema。
