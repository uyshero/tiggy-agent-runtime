# Tool 人工审批与恢复语义

## 边界

`approval` 包和 `agent.Resume` 提供进程内执行协议：在 Tool 产生副作用之前暂停，由嵌入应用收集人工决定后继续。Core 不识别审批人、不持久化决定、不发送通知，也不唤醒分布式 Worker。

审批与内置 Tool Permission 是两层控制：

- `approval.Policy` 在模型生成的整批 Tool Call 执行前做统一闸门；
- `permission.Policy` 仍是 Tool 自己执行的资源授权检查；
- 嵌入应用负责 UI、身份、存储、过期和消息投递。

未配置 `ApprovalPolicy` 时默认允许全部调用，保持向后兼容。

## 配置

```go
gate := approval.PolicyFunc(func(
    ctx context.Context,
    request approval.Request,
) (approval.Decision, error) {
    if request.Tool == "builtin.edit" {
        return approval.Decision{
            Behavior: approval.BehaviorRequire,
            Reason:   "编辑工作区文件需要人工确认。",
        }, nil
    }
    return approval.Decision{Behavior: approval.BehaviorAllow}, nil
})

runtime, err := agent.New(agent.Config{
    Name:           "assistant",
    Model:          provider,
    ModelIdentity:  identity,
    Toolkit:        kit,
    ApprovalPolicy: gate,
})
```

`Request.Arguments` 是独立复制的精确 JSON，Policy 可以据此判断副作用。Policy 不能把凭证或原始参数写入 `Reason`；Reason 会展示给操作员和模型。

## 暂停与恢复

同一模型响应产生的一批 Tool Call 中，只要有一个需要审批，整批都不会执行。`Run` 返回 `StatusSuspended`；State 保留 Pending Tool Call，`Suspension` 保存每个调用冻结后的处置方式。只有需要人工审批的调用具有 `ApprovalID`。

```go
result, err := runtime.Run(ctx, input)
if err != nil {
    return err
}
if result.Status == agent.StatusSuspended {
    // 应用可以在等待审批期间序列化 result.State。
    decisions := []approval.Resolution{{
        ApprovalID: result.Suspension.Calls[0].ApprovalID,
        Status:     approval.ResolutionApproved,
    }}
    result, err = runtime.Resume(ctx, result.State, decisions, sink)
}
```

`Resume` 要求每个 Pending Approval 恰好有一个 Resolution。缺失、重复或未知决定会返回错误，同时保留 Suspended State，调用方修正后可以重试。等待审批不占用 Runtime Goroutine，也不计入原 Run Context 的时长。

批准后按模型原始顺序执行。拒绝的调用不会执行，而是产生不可重试的 `approval_rejected` Tool Result，模型随后可以继续推理；Policy 直接拒绝产生 `approval_denied`。Resume 不会再次调用 ApprovalPolicy。

## 状态机视角

`StatusSuspended` 不是把一个 goroutine 挂在那里等待，也不是整项任务失败。它表示“本次 Runtime 调用已经安全终止，并返回了一个可验证的续跑点”。

```text
Run / Continue
  -> model emits one Tool batch
  -> ApprovalPolicy classifies every call
     -> all allow/deny: execute batch and continue loop
     -> any require: store Suspension, emit approval.required, return run.suspended

Resume
  -> validate suspended State
  -> validate exact approval resolutions
  -> validate current Toolkit definition digests
     -> mismatch or bad resolution: keep same Suspension and return suspended
     -> valid: clear Suspension, emit approval.resolved, execute original batch
```

这个状态机有三个重要不变量：

- 挂起前整批 Tool 都不能执行，否则审批只能保护部分副作用；
- 恢复前必须验证 State、Resolution 和 Toolkit 契约，否则审批通过的可能不是原来的调用；
- 决议错误或 Toolkit 不匹配时必须保留原 Suspension，否则用户没有机会修正并安全重试。

因此，`run.suspended` 是一次调用的终态：本次调用不会继续偷偷执行；但它不是业务任务的终态：应用可以持久化 `Result.State`，收集人工决定，然后发起新的 `Resume` 调用。

## 完整性与事件

Suspension 为每个调用绑定：

- Call ID 和 Tool Name；
- Tool Name 与精确参数 JSON 的 SHA-256；
- 冻结的模型可见 Tool Definition SHA-256；
- 原始当前 Turn 的起点。

修改 State 中的参数，或使用不同 Tool 契约恢复，都会在执行 Tool 前失败。Core 无法证明 Tool 实现背后的可变内部依赖没有变化，Adapter 必须维持自身执行契约稳定。

Toolkit 契约不匹配时返回 `approval_toolkit_mismatch`，并保留 Suspended State；此时不会标记审批已解决，也不会启动 Memory 或 Tool 操作，应用可以换用兼容 Runtime 后重试。直接篡改 Suspended State 则作为非法输入拒绝。

第一次调用产生 `approval.required`，以 `run.suspended` 终止。恢复是新的 Runtime 调用：`run.started` 携带 `continuation_id`，随后产生 `approval.resolved`、Tool 事件和唯一 Run 终止事件。审批事件只记录 Hash 和有界 Reason，不记录原始 Tool 参数。

Memory Recall 在 Resume 时重新执行，因为 Recall Evidence 只属于 Request View，不进入完整 Agent State。Suspended Turn 只有在最终完成后才会被 Memory Observe。

## 持久化边界

`State` 和 `Suspension` 是可以 JSON 序列化的 Continuation Value，不是持久化调度器。需要落库的应用必须自己实现原子存储、授权、加密、保留与过期、单恢复者协调和 Worker 唤醒。Core 仍不承诺进程崩溃恢复或外部 Tool 副作用 Exactly-once。
