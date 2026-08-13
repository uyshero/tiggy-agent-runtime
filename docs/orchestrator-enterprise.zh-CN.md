# Orchestrator Enterprise 接入

本契约用于保持 `tiggy-agent-runtime` 的可复用性，同时允许
`tiggy-manage-agent` 或其他托管平台持久运行 Brief。依赖方向必须单向：

```text
tiggy-manage-agent/internal/runtimeadapter
  -> github.com/uyshero/tiggy-agent-runtime/orchestrator

tiggy-agent-runtime
  -X-> Enterprise Package、Schema、API、Tenant、Queue 或 Worker
```

Core 负责可移植执行协议：

- 严格校验 Brief，并按确定顺序执行任务；
- 每个任务构造一个隔离 Subagent，并且只产生一个 Artifact；
- 校验输出，只投影显式声明的输入 Artifact；
- 提供 opaque Artifact 引用、`ExecutionSnapshot`、Checkpoint CAS 和
  `Resume` 校验；
- 发射不含正文的生命周期 Event 和结构化错误。

Enterprise 负责运行权威：

- Tenant 与 Workspace 授权；
- Execution 记录、Queue、重试策略和终态事务；
- Worker 注册、容量、轮询、关闭与可观测性；
- Lease 获取、Heartbeat、过期、Fencing Token 和孤儿任务恢复；
- 持久化 Artifact/Checkpoint Adapter、加密、保留和清理；
- 不确定外部副作用的对账与处置。

## Adapter 形态

只允许 Enterprise 的 `runtimeadapter` 包导入 Core。该包把 Enterprise 记录转换为
Core Value，并实现以下注入 Port：

| Core Port | Enterprise Adapter 职责 |
| --- | --- |
| `SubagentFactory` | 使用已授权的 Model、Toolkit、Memory Scope、SafeSpace、Credential 和 Limit，为一个任务创建 Agent。 |
| `Store` | 在授权 Tenant/Workspace 内保存 Artifact；遵守 `PutRequest.IdempotencyKey`；只返回不含 Signed URL 的 opaque Reference。 |
| `CheckpointStore` | 加载已授权 Execution；提交时同时原子比较 Snapshot Version 和当前 Fencing Token。 |
| `Sink` | 持久化或发布不含正文的生命周期 Event，但不能把 Event 当作恢复权威。 |
| `agent.TransitionHook` | 当任务会调用 Model 或 Tool 时，保存细粒度 Agent 边界和加密的私有恢复数据。 |

`ExecutionSnapshot`、`ArtifactReference` 和 Event Payload 故意不包含 Lease
数据。Adapter 实例在构造时就应绑定 Enterprise Execution Scope 与 Lease Claim，
从而避免 Core 协议耦合某个数据库 Schema 或租户模型。

## Worker 流程

```text
领取 Queue Item，并取得 Lease Epoch N
  -> 创建可取消的 Execution Context
  -> 构造绑定 Lease 的 Runtime Adapter
  -> 启动 Heartbeat；续租失败时取消 Context
  -> 加载 Brief，并选择 Run 或 Resume
  -> 执行 Core Orchestrator
  -> WHERE lease_epoch = N 提交 Enterprise 终态
  -> 释放 Lease，并确认 Queue Item
```

即使 Worker Context 尚未收到取消，Checkpoint Adapter 也必须拒绝每一次过期写入。
典型 SQL 事务应在更新 Snapshot 前同时检查：

```text
execution_id 匹配
tenant/workspace scope 已授权
lease owner 与 fencing epoch 仍为当前值
lease 尚未过期
checkpoint version 等于 expected
```

Artifact Adapter 应对 `Put` 应用相同的授权检查，并在后端支持时应用 Fencing
Guard；`Get` 必须验证 opaque Reference 属于当前绑定的 Execution Scope。每任务稳定
Idempotency Key 允许重试复用“Artifact 已写入、Checkpoint 尚未提交”的对象。

## 失败语义

- Heartbeat 失败会取消 Core Context，但取消是协作式的；忽略 Context 的 Model、
  Tool 或 Subagent 仍可能继续运行。
- Checkpoint CAS 是进度提交权威。它能阻止过期进度提交，但不能阻止两个 Worker
  同时进入同一个 Subagent。
- Lease 丢失后必须禁止 Checkpoint、Enterprise 终态和外部可见结果提交，不能把它
  转换为成功完成。
- 外部 Tool 副作用仍需要稳定幂等键、查询、补偿或人工对账；Lease 与 Checkpoint
  CAS 都不能产生 exactly-once 效果。
- 已完成 Snapshot 可以在不解析 Subagent 的情况下恢复；部分 Snapshot 会在开始下个
  任务前重新校验全部 Artifact。

## 仓库规则

Core 不得包含 Worker Loop、SQL Migration、Queue Consumer、Tenant 类型、HTTP
控制面 Handler 或 Enterprise Import。Enterprise 应通过单一 Adapter 包依赖已发布的
SemVer Runtime，不应复制 Core 代码或依赖相邻源码目录。Enterprise Contract Test
至少覆盖：过期 Fencing 拒绝、失租取消、重复投递、Artifact 幂等、进程退出后恢复、
以及终态提交冲突。
