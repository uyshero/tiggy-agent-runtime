# Agent Runtime 语义

## API 与所有权

- `agent.New` 校验配置并生成不可变 Agent；当注入的 Model 和 Tool 支持并发时，同一个 Agent 可被多个调用方并发复用。
- `Run` 从空 State 开始，`Continue` 只接受没有未完成 Tool Call 的完整 State；`Resume` 只接受通过校验的审批 Suspension 和精确完整的决定集合；`Recover` 只接受持久化的 Model/Tool completed Transition，并要求配置 Transition Hook。
- 每次 Run 复制输入 State、Model Request、Toolkit 定义和返回结果；调用方不能通过保留 Slice 修改运行中状态。
- Toolkit 在 Run 开始时创建快照。Run 期间注册的新工具只对后续 Run 可见。
- 激活 Skill 是不可变 Agent 配置，其指令只进入初始 Instruction Stack System Message，并带有 `metadata.kind=instruction_stack`；`run.started` 只记录 Name/Version/Digest 来源，不记录内容正文。
- 可选的每 Model Context Planner 在 Provider Call 前运行。Overflow 在 Model Call 之前终止；同 Target Retry 复用未变 Plan，Fallback 使用自己的 Planner。
- 可选的每 Model Context Compressor 只能在 Provider Request View 中替换 Core 选择的非活跃历史组，完整 State 保持不变。
- 可选 Artifact Offloader 在成功 Tool Call 之后、Result 进入 State 之前运行。Offload 后 State 保存 Digest-bound Reference，完整 Result 保留在注入 Store 中。
- 可选 Memory Port 在 Model Execution 前 Recall 一次，在成功 Response Commit 后 Observe 一次。Recall Evidence 只改变 Model Request View；Observe 只接收当前 Turn 新增的 Message。
- `Result.State` 是本次运行的独立快照，可用于应用自己管理的后续 `Continue` 或 `Resume`；仅做序列化不等于具备持久化调度。

## 执行与失败

Runtime 顺序执行 Model 和 Tool，直到模型完成、取消、失败或达到边界。当前边界包括迭代次数、模型调用次数、工具调用次数、累计 Token、单次输出 Token、单次调用流式 Delta 数量/字节和总时长。

总时长通过 Context 传播到 Model 和 Tool。Event Sink 是调用方代码，必须及时返回或自行实现有界 I/O；Runtime 无法抢占一个阻塞且不返回的回调。

`MaxModelDeltasPerCall` 和 `MaxModelDeltaBytesPerCall` 在 Delta 进入 Event Sink 或 `Result.Events` 之前检查；字节预算统计 Delta 所有可变长字符串字段的 UTF-8 字节，且不会为统计复制正文，固定事件信封开销由数量上限约束。任一预算超限都会以 `limit/max_model_deltas` 或 `limit/max_model_delta_bytes` 终止 Run。首次超限会被锁存，因此异常 Provider Adapter 即使忽略 DeltaSink Error，也不能继续扩大诊断事件轨迹；但它仍可能占用自己的 `Generate` 调用，直到返回或响应 Context 取消。Runtime 会串行化并发回调，并在 `Generate` 返回时关闭 Delta 入口，防止晚到回调改变事件序列。这两个新增字段为零时自动使用默认值，使旧版显式 `Limits` 保持兼容；负值会被拒绝。

Tool 失败分为两类：

- `tool.Error` 是已脱敏、模型可处理的失败，被写为 `ToolResult(IsError=true)`，Run 继续；
- 普通 error、panic、非法 Tool Result 属于 Runtime Failure，写入安全的中断 Tool Result 后终止 Run。

Completed、Failed 和 Canceled 路径都会关闭未完成 Tool Call。`StatusSuspended` 会有意保留一个通过 Digest 绑定的 Pending Tool Batch，并以 `run.suspended` 终止当前调用，详见 [Tool 人工审批与恢复语义](./approval.zh-CN.md)。

可选 `ApprovalPolicy` 会在模型生成的整批 Tool Call 执行前完成判断。只要其中一个需要人工决定，整批零副作用暂停。Resume 校验参数与 Tool Definition Digest，按原始顺序应用全部决定，且不会再次调用 Policy。拒绝会成为模型可见的 Tool Error，而不是 Runtime Failure。

Artifact Offload 只处理成功 Tool Result；模型可处理的安全 Tool Error 保持内联。已配置 Offloader 接收独立、版本化 Payload，每个 Result 最多调用一次；Store/Offloader 失败、非法 Decision、Payload Limit 和取消都不会退回为静默内联一个违反边界的大结果。Runtime 写入明确 Error Tool Result 后终止。由于取消可能与已完成 Put 竞争，Store 可能留下未引用对象；Core 不自动执行 Delete。

`tool.result.offload.started` 与 `tool.result.offload.completed`（`inline`/`offloaded`）或 `tool.result.offload.failed`（`failed`/`canceled`）配对。Event 记录 Size、Media Type、Digest、opaque Object Reference 和 Fallback，不记录 Result Body。

Memory Recall/Observe 接收独立 Value 和 Run Context。普通 Adapter Failure 默认 Degrade，也可配置为 `fail_run`。Cancellation 始终取消 Run；Panic、非法/跨 Scope Recall Result 和 Recall Limit Violation 始终 Fatal。Agent 在每次模型投影前过滤过期 Recall Item。Observe 只在 Final Assistant Response 及 Model Call Completed Event 之后开始；Degraded Observe 保留 Completed Result。Memory Event 暴露 Active/Expired Count 和 Namespace，不暴露 Text、Observed Message、Expiry、Provenance Correlation 或 Scope Key。

新 Runtime 的每个 `model.call.started` Event 都携带可选的无正文 Context Manifest，对该次物理 Provider Request 记录固定数量的 Message/Role/Visibility/Projection/Content/Tool Definition 计数与字节，包括压缩前和最终 Message 数。旧 Protocol V1 Event 可以缺失该字段。详见 [Context 诊断快照](./context-diagnostics.zh-CN.md)。

## Model Retry 与 Fallback

默认 Failure Policy 是 fail-fast，不会隐式重试。需要瞬时故障恢复时，调用方可以配置 `TransientModelFailurePolicy` 和有序 `Fallbacks`：

```go
agent.Config{
    Model: primary,
    ModelIdentity: primaryID,
    Fallbacks: []agent.ModelTarget{{Model: backup, Identity: backupID}},
    FailurePolicy: agent.TransientModelFailurePolicy{
        MaxAttemptsPerTarget: 2,
        BaseDelay: 200 * time.Millisecond,
        MaxDelay: 2 * time.Second,
        EnableFallback: true,
        EnableContextRecovery: true,
    },
}
```

- 内置 Policy 只在 Provider Adapter 明确设置 `ProviderError.Retryable=true` 时重试普通失败；
- `EnableContextRecovery` 是独立的显式开关：当 Target 同时有 Planner 和 Compressor 时，`ErrorContextLength` 最多触发一次结构安全压缩和同 Target 重试；
- Retry 使用相同 State 和 Model Target，但生成新的 Call ID；Fallback 切换到下一个显式配置的 Model Target；
- 每次物理尝试都计入 `MaxModelCalls`，所以自定义 Policy 也无法无限重试；
- 退避等待响应 Context 取消，并停止、清空内部 Timer；
- `retry.scheduled` 和 `fallback.activated` 记录失败 Call、目标、原因与延迟；
- Model 可能在失败前已产生 Delta，消费者必须按 Call ID 区分尝试；
- Provider Adapter 必须在 DeltaSink 返回 Error 后停止流式生成；即使 Adapter 违反该约定，Core 仍会限制保留的 Delta 事件；
- Tool 不自动重试，因为 Runtime 无法假设外部副作用可重复执行。

自定义 `ModelFailurePolicy` 必须并发安全。非法 Decision 或 panic 会被隔离为 `model_failure_policy_failed`，不会破坏 State。

Model、Tool、Memory、Failure Policy 和 Event Sink 的 panic 都在 Runtime 边界转换为结构化失败。注入 Port 是调用方管理的长生命周期依赖，Runtime 不负责关闭它们；Adapter 必须在每次调用返回前释放自己创建的响应体、Stream 和临时资源，并响应 Context 取消。

## Durable Transition Hook

可选 `Config.TransitionHook` 在 Model、Tool effect、State commit 和审批 Resume 的关键边界同步提交检查点。Hook 成功前 Runtime 不进入下一外部效果，也不替换候选 State；Hook error 或 panic 产生 `transition/*` RunError 并失败关闭。

Transition JSON 元数据只有 ID、Run-local Sequence、State/Tool/Arguments Digest、Effect Class、Outcome 和幂等键。完整 State、Model Request/Response 与 Tool Arguments/Result 通过 `json:"-"` 私有字段交给平台加密保存。未声明 Tool effect class 时默认 `non_reconcilable_write`。稳定 Tool 幂等键还会注入 Execute Context，可由 Adapter 通过 `ToolTransitionFromContext` 传给外部 API。

`Recover` 允许 Core 校验 completed Transition 中的精确私有结果并推进 State 后继续执行。它不接受调用方拼装的 State 或人工成功断言。恢复 Tool Result 后，同批剩余调用会进入新的审批 Suspension。

详细顺序、恢复矩阵和安全规则见 [Durable Transition Hook](./transition.zh-CN.md)。

Context Planning 使用同一取消边界。Counter/Planner 失败会终止 Run，因为继续执行会破坏已配置的 Context 保证。`context.prepare.started/prepared` 在 Ready、Overflow、Error 和 Cancellation 路径均保持配对。

发生 Overflow 后，已配置 Compressor 只有一次机会，随后必须重新规划。普通同 Target Retry 复用压缩视图；Fallback Target 从完整 State 重新开始，并使用自己的 Planner 和可选 Compressor。显式开启后，未被预检发现的 Provider Context Length 拒绝可对失败 Request View 压缩一次、重新规划，并占用一次 `MaxModelCalls`；第二次拒绝不再压缩。Compression Error、非法 Decision、取消、压缩不足和 Event Sink 失败全部 Fail-closed，并在终止前产生配对的 `context.compression.started/completed|failed` Operation。

## Event Sink

Sink 同步接收严格有序的 Event。回调返回 error 表示消费者不能继续，Runtime 立即停止，不再向该 Sink 投递事件。`Result.Events` 仍保存 Runtime 内部完成的诊断轨迹，并保证操作 Start/Completed 配对和恰好一个终止事件，包括 `run.suspended`。

Sink 失败语义：

- 终止事件之前失败：`Result.Status=failed`，返回 `event_sink/event_sink_failed`；
- Runtime 已经完成，但 Sink 拒绝终止事件：保留真实执行状态和 Final，同时返回 `event_sink/terminal_event_failed`。

第二种情况区分“Agent 执行失败”和“结果通知失败”。调用方不能只检查 Status，必须同时检查返回 error。

可选的 `observe` 包在该 Sink Boundary 上组合只读 Consumer。直接 Sink 仍是严格模式；当 Logging 或 Telemetry 不能导致执行失败时，`observe.BestEffort` 会隔离 Observer Error 和 Panic，但不会把阻塞 Callback 自动变成异步。详见 [可观测性语义](./observe.zh-CN.md)。

可选的 `record.Recorder` 是另一个严格 Sink Consumer。它增量写入每个已接受 Event，因此在 Terminal Event 前执行或进程终止时，Journal 仍可保留结构有效的 Partial Run。Recording Failure 遵循相同 Sink Rule，且不会自动 Retry Append。详见 [Run Recorder 语义](./record.zh-CN.md)。

## 当前非目标

State 和 Event 只提供进程内正确性与诊断。可选 Transition Hook 与 `Recover` 可应用已确定完成的持久化结果，但 Core 仍不提供数据库、最新边界选择、单恢复者协调、indeterminate Tool 自动重放、外部副作用 Exactly-once 或 Durable Worker State Machine。企业级落库和 Worker 状态机可参考 [Enterprise Runtime 持久化参考设计](./enterprise-runtime.zh-CN.md)。
