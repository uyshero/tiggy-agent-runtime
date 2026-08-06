# Context Governance：上下文治理蓝图

## 目标

Tiggy Agent Runtime 的上下文治理目标不是“把所有东西都塞进 Prompt”，而是让每次模型调用都能回答三个问题：

1. 哪些内容必须始终保留？
2. 哪些内容可以压缩、外置、按需召回或丢弃？
3. 如果模型表现异常，能否解释这次请求到底由哪些部分组成？

这层设计服务于稳定运行、可监控、异常诊断和可扩展性。它不要求 Core 拥有数据库，也不把企业级任务队列、租户策略或长期记忆存储塞进 Runtime。

## 分层模型

推荐把 Agent 上下文分成四层：

| 层级 | 含义 | Runtime 当前承载方式 | 处理原则 |
|---|---|---|---|
| 固定指令 | 系统规则、项目规则、任务规则、策略、已激活 Skill 指令 | `SystemPrompt`、`agent.Config.Instructions`、Skill Activation、Instruction Stack | 始终保留，位于动态上下文边界之上 |
| 结构化任务状态 | 当前目标、计划、待办、暂停/续跑状态、未完成 Tool Batch | `agent.State`、`StatusSuspended`、`Suspension`、`builtin.todo`、Tool Call/Result 关系 | 用结构化数据表达，不依赖旧对话自然语言猜测 |
| 短期工作记忆 | 当前 Run 的近期对话、活动 Tool 关系、压缩摘要 | `contextmanager.Planner`、`Compressor`、完整 State 与 request-only 压缩视图 | 旧历史可以压缩，但当前活动工作和 Tool Call/Result 配对不能破坏 |
| 外部长期记忆 | 用户偏好、项目知识、历史事实、完整大型结果 | `memory.Port`、`artifact.Offloader`、应用自有存储 | 按当前步骤召回相关片段；完整正文留在外部 |

这四层不是为了显得架构复杂，而是为了避免一种常见失败：上下文越跑越长，模型越来越不稳定，但系统无法说明到底是指令膨胀、Tool Schema 膨胀、记忆召回过多，还是压缩摘要失真。

## 固定指令：始终保留

固定指令代表更高权威，应该稳定、可审计、可预算。

在 Tiggy 中，它们进入 Instruction Stack：

```text
SystemPrompt
Configured Instructions
Activated Skill instructions

=========== DYNAMIC CONTEXT BELOW ===========
```

动态边界的作用是告诉模型：边界之下是证据和运行期上下文，不应该覆盖上方规则。

相关能力：

- `contextassembly.Render`
- `agent.Config.SystemPrompt`
- `agent.Config.Instructions`
- Skill Activation
- `model.call.started.context.InstructionStackBytes`
- `agenttest.ValidateInstructionStackBudget`

设计要求：

- 固定指令不能被 Compressor 任意改写；
- Skill 指令必须显式激活后才进入上下文；
- 诊断日志只暴露字节数和类型聚合，不暴露正文、来源路径或 Skill 名称。

## 结构化任务状态：不要藏在旧对话里

Agent 如果只靠自然语言历史维持任务状态，会出现两个问题：

- 历史一压缩，任务细节可能被摘要扭曲；
- 恢复或审批后，系统难以判断“现在到底卡在哪一步”。

因此任务状态应该尽量结构化：

- Run 当前状态：`completed`、`failed`、`canceled`、`suspended`
- 暂停续跑：`SuspensionID`、待审批 Tool Batch、Digest 校验
- 工具关系：Tool Call 与 Tool Result 必须配对
- 待办状态：`builtin.todo` 的 `Result.State` 与 `ExtractLatestTodoState`

`run.suspended` 是一次调用的终态，但不是整个业务任务失败。它表示 Runtime 已经安全停在“外部副作用执行前”的边界上，等待应用或人类审批后再 `Resume`。

设计要求：

- 当前目标、约束、未完成动作必须能从结构化 State 中恢复；
- 审批前不能部分执行 Tool Batch；
- Recorder 只能诊断历史事件，不能代替可恢复 State。

## 短期工作记忆：压缩的是 Request View，不是完整 State

短期工作记忆包括近期对话、活动 Tool 关系和压缩摘要。它的核心原则是：

> 可以改变模型看到的历史视图，但不能改写 Runtime 持有的完整 State。

这就是 Tiggy Compressor 的边界：

- Compressor 不能随便返回任意 Message 列表；
- 它必须按语义组覆盖旧消息；
- 当前活动 Tool Call/Result 关系必须保护；
- 压缩后需要重新规划预算；
- 如果压缩结果非法，Runtime fail-closed 或按策略降级。

相关能力：

- `contextmanager.Planner`
- `contextmanager.Compressor`
- `context.prepared`
- `context.compression.*`
- `evaluation/compression`

设计要求：

- 目标、约束和当前状态优先保留；
- 旧对话压缩为结构化摘要；
- 摘要必须有覆盖范围和可验证边界；
- 重复日志、冗余输出和过期上下文应优先丢弃或外置。

## 外部长期记忆：按需召回，不写回对话 State

长期记忆适合放在 Core 外部，因为不同应用的存储、租户隔离、删除策略、加密和合规要求差异很大。

Tiggy Core 只定义边界：

- `memory.Port`：Recall / Observe
- `memory.Scope`：由应用定义作用域
- `memory.Options`：召回数量、字节上限、失败策略
- `artifact.Offloader`：大型 Tool Result 外置为引用

Memory Recall 是 request-only：

- 召回内容只进入当前模型请求；
- 不追加到 `Agent.State`；
- 诊断事件只记录数量和字节；
- 不暴露 Scope Key、Memory Item ID 或正文。

Artifact 引用用于避免完整 Tool Result 无限占用上下文。模型看到的是有摘要和 Digest 绑定的引用，完整对象由应用拥有的 Store 保存。

设计要求：

- 完整 Tool Result 优先外置；
- 每次只召回当前步骤相关片段；
- 长期记忆的写入、删除、租户隔离和持久化属于应用或企业实现；
- Core 不承诺数据库 durability。

## 预算、检查点与引用

上下文治理必须有“水位线”，否则很难知道系统什么时候开始变坏。

当前 Runtime 提供三类信号：

| 信号 | 用途 | 对应能力 |
|---|---|---|
| Token 预算 | 判断 Provider Window 是否能容纳请求 | `context.prepared`、`TokenPlanner` |
| 内容无关 Manifest | 观察请求形态，不泄露正文 | `model.call.started.context`、`observe.LogSink` |
| 测试预算门禁 | 防止回归引入上下文膨胀 | `agenttest.ValidateInstructionStackBudget`、`agenttest.ValidateContextBudget` |

建议测试中至少设置这些预算：

- 固定指令最大字节数；
- Tool Definition 最大字节数和数量；
- Recall Memory 最大条数；
- Compression Summary 最大条数；
- Request Message 最大数量；
- Tool Result / Artifact Block 最大数量；
- Text / Tool Argument 最大字节数。

检查点分两种：

- Core 内的过程检查点：`State`、`Suspension`、Events、Recorder；
- 企业实现的持久检查点：数据库 Run State、Worker Lease、Approval Record、Artifact/Memory Store。

Core 提供可序列化、可校验的边界；真正的跨进程恢复和数据库事务属于企业层。

## 诊断路径

一次模型异常时，推荐按这个顺序排查：

1. `memory.recall.*`：召回是否过多、降级、过期过滤？
2. `context.prepared`：是否接近模型窗口？是否预留了输出？
3. `context.compression.*`：是否触发压缩？覆盖了哪些消息组？
4. `model.call.started.context`：最终请求由多少 Message、Tool Schema、Memory、Summary、Artifact 组成？
5. `tool.result.offload.*`：大结果是否外置成功？
6. `retry.scheduled` / `fallback.activated`：是否因为失败策略产生新调用？
7. 受控测试中的 `ScriptedModel.Calls()`：必要时查看完整 Request 正文。

生产日志不要打印完整 Prompt、Memory 或 Tool Result。完整上下文只应出现在受控测试或有明确授权的调试环境。

## 反过度设计边界

这套治理不是要求 v0.1.x 做完整企业平台。Core 当前应该坚持：

- 不内置数据库；
- 不内置企业 Worker；
- 不承诺 exactly-once 外部副作用；
- 不自动重放 MCP Tool；
- 不把 Recorder 当恢复引擎；
- 不把所有 Tool / MCP / Skill Schema 一次性全部暴露；
- 不把长期记忆正文写入完整 Agent State。

更务实的判断标准是：

> Core 负责让一次 Agent 调用边界清楚、失败可解释、状态可校验、上下文可预算；企业层负责持久化、调度、租户、安全审计和跨进程恢复。

## 当前差距

已经具备：

- 固定指令与动态上下文边界；
- Memory request-only 投影；
- Artifact 外置引用；
- 结构化 Compressor 合约；
- Context Manifest；
- 预算测试 Helper；
- Recorder 与 Observer 诊断；
- 审批暂停和续跑句柄。

仍可继续增强：

- 更统一的 Runtime-owned Task State 抽象；
- Dynamic Capability Activation，把 Tool / MCP / Skill 从静态 Group 选择推进到运行期按需激活；
- Memory Recall 的引用与 provenance 诊断更细；
- Compression 摘要质量门禁与事实保持评测继续丰富；
- 企业层持久检查点与 Core State/Suspension 的参考实现。

