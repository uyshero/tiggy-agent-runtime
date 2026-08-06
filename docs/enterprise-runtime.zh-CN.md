# Enterprise Runtime 持久化参考设计

本文说明企业级服务如何接住 `agent.State`、`approval.Suspension` 和 Runtime Event，构造数据库级 Agent 执行状态机。它是上层应用参考，不是 Core API 承诺。

Core 只提供进程内正确性：

- `Run` / `Continue` / `Resume` 是一次函数调用；
- `Result.State` 是可序列化的续跑值；
- `StatusSuspended` 表示本次调用安全终止，并留下审批续跑点；
- Event 和 Recorder 可用于诊断，但不拥有恢复执行的权力。

Enterprise 需要额外提供持久化、调度、审批身份、租户隔离、审计、Worker 协调和崩溃恢复。

## 推荐状态机

```text
queued
  -> running
     -> suspended
        -> resuming
           -> running
     -> completed
     -> failed
     -> canceled

running/resuming
  -> retry_waiting
  -> failed
```

核心规则：

- `running` / `resuming` 必须由 Worker Lease 或乐观版本保护；
- `suspended` 不占用 Runtime goroutine，也不占用 Worker；
- 审批通过后不要直接执行 Tool，应先把数据库状态从 `suspended` 原子改成 `resuming`；
- `completed`、`failed`、`canceled` 是业务任务终态；
- `run.suspended` 只是一次 Runtime 调用的终态，不是业务任务终态。

## 最小数据模型

下面是概念模型，不要求 Core 引入这些类型。

| 表/记录 | 关键字段 | 职责 |
| --- | --- | --- |
| `agent_runs` | `id`、`tenant_id`、`status`、`state_json`、`suspension_json`、`runtime_version`、`toolkit_digest`、`version`、`lease_owner`、`lease_until` | 保存任务当前状态、续跑值、并发控制和恢复入口。 |
| `approval_items` | `run_id`、`approval_id`、`call_id`、`tool`、`arguments_digest`、`tool_digest`、`status`、`decided_by`、`decided_at`、`reason` | 保存企业审批记录、审批人、时间和审计信息。 |
| `run_events` | `run_id`、`sequence`、`event_type`、`event_json`、`created_at`、`redaction_version` | 保存可查询时间线；可由 Observer 或 Recorder Journal 导入。 |
| `tool_effects` | `run_id`、`call_id`、`tool`、`status`、`started_at`、`finished_at`、`idempotency_key`、`external_ref` | 跟踪外部副作用，用于崩溃后判定是否需要补偿、查询或人工介入。 |

`state_json` 和 `suspension_json` 应加密或按租户隔离保存，因为其中可能包含对话内容、工具调用参数摘要、消息 ID、Artifact 引用和模型可见上下文结构。

## 挂起落库流程

```text
Worker owns lease
  -> calls runtime.Run / Continue
  -> receives StatusSuspended
  -> within one DB transaction:
       update agent_runs.status = suspended
       store Result.State
       store Result.Suspension
       insert approval_items for required calls
       append terminal run.suspended event
       clear lease
  -> notify approval system
```

注意：通知审批系统应该在数据库提交后进行。否则通知已经发出，但续跑 State 没有持久化，审批人点通过时系统找不到可靠的恢复点。

## 审批恢复流程

```text
Approver submits decisions
  -> verify identity, tenant, policy, expiry
  -> transactionally mark approval_items decided
  -> atomically move agent_runs suspended -> resuming
       where version matches and all required approvals are decided
  -> enqueue Worker

Worker owns lease
  -> loads state_json and decisions
  -> calls runtime.Resume
     -> StatusSuspended with error: keep DB suspended or operator_required
     -> StatusCompleted: store final State and complete
     -> StatusFailed/StatusCanceled: store terminal status
```

如果 `Resume` 返回 `approval_toolkit_mismatch` 或 `invalid_approval_resolution`，不要丢弃旧的 `State` / `Suspension`。这说明恢复输入不满足 Core 不变量，上层应保留原始续跑点，并进入可修正状态。

## 崩溃恢复边界

Enterprise 不能只靠 `Result.State` 宣称 durable execution。必须覆盖这些崩溃点：

- 模型请求发出前后；
- Tool 开始前、执行中、返回后、结果落库前；
- `run.suspended` 返回后、审批记录落库前；
- 审批决议写入后、Worker 被唤醒前；
- Worker 获得 Lease 后进程崩溃；
- 当前 Toolkit 与挂起时 Digest 不匹配；
- Recorder / Observer 写入失败。

对外部副作用，推荐 Tool Adapter 接收企业生成的 `idempotency_key`，并在外部系统可查询时保存 `external_ref`。如果外部系统不支持幂等或查询，崩溃后应进入人工处置，而不是自动重放 Tool。

## 与 Core 的分工

| 能力 | Core | Enterprise |
| --- | --- | --- |
| 一次调用的状态校验 | 是 | 调用并处理结果 |
| 可序列化 `State` / `Suspension` | 是 | 加密、落库、版本管理 |
| 审批前零副作用挂起 | 是 | 审批人、通知、过期、审计 |
| 单恢复者协调 | 否 | DB version、lease、queue |
| 崩溃恢复 | 否 | Worker recovery、effect reconciliation |
| 外部副作用 Exactly-once | 否 | 幂等键、外部查询、补偿或人工介入 |
| 时间线查询 | 提供 Event | 持久化、索引、权限、UI |

一句话：Core 负责“这一次调用有没有做对”；Enterprise 负责“这个任务跨进程、跨人、跨时间能不能可靠地活下去”。
