# Durable Transition Hook

`agent.Config.TransitionHook` 为托管平台提供数据库无关、同步且失败关闭的执行检查点协议。它解决
“只能在 Invocation 返回时拿到完整 `Result.State`”的问题，但不在 Core 中引入数据库、Queue、Lease、
租户或审批身份模型。

Hook 是可选能力。未配置时，`Run`、`Continue` 和 `Resume` 保持原有进程内语义；`Recover` 必须配置 Hook。

## Transition 类型

单次 Runtime Invocation 使用独立的 Run-local `Sequence`，并为每个 Transition 生成稳定 ID：

| 类型 | 持久化边界 |
| --- | --- |
| `model_call.prepared` | Provider Request 已冻结，但尚未调用 Model。 |
| `model_call.completed` | Model 已返回；私有载荷可包含最终 Response。 |
| `tool_batch.frozen` | Tool Call、参数摘要、定义摘要、效果类别和幂等键已冻结。 |
| `tool_effect.prepared` | 单个 Tool effect 已准备，但尚未开始。 |
| `tool_effect.started` | Runtime 即将调用 Tool。崩溃后如果没有终局记录，结果必须视为未知。 |
| `tool_effect.completed` | Tool 已返回确定且 State-ready 的结果；可恢复时私有载荷包含精确投影 Result。 |
| `tool_effect.indeterminate` | 取消、Runtime Failure 或完成记录失败导致外部结果不确定。 |
| `state_transition.committed` | 候选 State 已通过校验，Hook 成功后 Runtime 才替换内存 State。 |
| `resume_continuation.consumed` | 审批决议已精确校验；Hook 成功后才清除 Suspension。 |
| `recovery_continuation.applied` | 已保存的确定结果通过校验并进入候选 State，之后 Runtime 才继续执行。 |

`Transition.StateDigest` 是完整 `Checkpoint` 的 SHA-256。`Transition.Validate` 会重新计算摘要，拒绝
正文与摘要不匹配的值。

## 配置

```go
value, err := agent.New(agent.Config{
    Name: "managed-agent",
    Model: provider,
    ModelIdentity: model.Identity{Provider: "openai", Model: "gpt"},
    Toolkit: kit,
    TransitionHook: checkpointStore,
    ToolEffectClasses: map[string]agent.ToolEffectClass{
        "crm.lookup": agent.ToolEffectReadOnly,
        "ticket.upsert": agent.ToolEffectIdempotentWrite,
        "payment.capture": agent.ToolEffectReconcilableWrite,
    },
})
```

未声明的 Tool 使用最保守的 `non_reconcilable_write`，不会因为缺少配置被误判为可安全重放。效果类别
只有四种：`read_only`、`idempotent_write`、`reconcilable_write` 和
`non_reconcilable_write`。

Hook 必须并发安全，并在自己的实现内限制数据库或网络 I/O。返回 error 或 panic 会产生
`transition/*` RunError，Runtime 停止进入下一 Model/Tool effect。Hook 不会被 Best Effort 降级。

## 元数据与私有载荷

Transition 的 JSON 元数据只包含 ID、序号、摘要、效果类别、Outcome 和幂等键，不包含 Prompt、Tool
参数或结果正文。

`Transition.Checkpoint` 与 `Transition.Private` 带 `json:"-"`：

- `Checkpoint` 是独立的完整 `agent.State` 副本；
- `Private.ModelRequest` / `ModelResponse` 保存需要加密的模型恢复材料；
- `Private.ToolArguments` / `ToolResult` 保存需要加密的 Tool 恢复材料；
- `Private.ToolResultStateReady`、`ToolResultIsError` 和 `ToolResultRetryable` 保存私有的 State 投影语义，必须与 Result 一起持久化。

平台必须按租户隔离并加密这些值。不要把完整 Transition 用反射日志、`%+v` 或自定义通用编码器写入
普通日志；`json.Marshal` 只会输出无正文元数据。

## Tool 幂等键

Tool 幂等键由来源 Assistant Message ID、Call ID、Tool Definition Digest、Arguments Digest 和效果类别
确定性生成。审批挂起前后及新的 Resume Invocation 会得到同一个键。

Runtime 还通过 Tool 的 `context.Context` 注入相同元数据：

```go
metadata, ok := agent.ToolTransitionFromContext(ctx)
if ok {
    request.IdempotencyKey = metadata.IdempotencyKey
}
```

直接调用 `toolkit.View.Call` 不经过 Agent，因此没有该 Context 值。

## Recovery API

平台选出最新的持久化边界，把 owned completed Transition 交回 Core，不直接构造下一份 `agent.State`：

```go
result, err := value.Recover(ctx, completedTransition, sink)
```

`Recover` 只接受 outcome 为 `completed` 的 `model_call.completed`，或 outcome 为 `succeeded` / `tool_error`
且带精确私有结果的 `tool_effect.completed`。它会重新校验 Checkpoint Digest、Model Response 语义、Tool Call
顺序、参数、当前 Tool Definition、Effect Class、幂等键、Result 结构和 Usage Limit。

进入下一次 Model/Tool effect 前，Core 同步提交 `recovery_continuation.applied`。它的幂等键由来源
Transition 稳定生成。Hook 必须保证一次逻辑应用；重复提交只有在 State Digest 完全相同时才能按幂等成功处理，
否则必须拒绝。

恢复 Model Response 后沿正常审批和 Tool Pipeline 继续。恢复 Tool Result 后，如果该结果结束整个批次，
Runtime 进入下一次 Model 调用；如果同批仍有调用，Core 会把所有剩余调用重新放入审批 Suspension，绝不因
重新评估策略而静默自动执行。

## 恢复规则

- 最后记录是 `tool_effect.prepared`：外部效果尚未开始，可以重新调度；
- 最后记录是 `tool_effect.started`：不得自动重放，除非效果类别和外部系统证明可安全处理；
- Model/Tool completed 后没有对应 State commit：把保存的 completed Transition 交给 `Recover`；
- `tool_effect.indeterminate`：只读/幂等写可按平台策略恢复，可对账写先查询外部引用，不可对账写进入人工处置；
- 重复 Worker 唤醒必须由平台 Lease/Fencing/乐观版本阻止，不能依赖进程内 Hook；
- `resume_continuation.consumed` 的幂等键绑定 Suspension ID，平台必须拒绝第二个有效消费者。

Core 不自动重放 Tool，不提供“人工确认成功后直接伪造 Tool Result”的入口，不负责判断哪个持久化边界
最新，不协调 Worker，也不承诺外部效果 exactly-once。`v0.2.0` 中启用 Artifact Offload 后产生的 Tool
completion 位于 State-ready 投影之前，因此不能自动恢复；`v0.2.1` 的新 completion 才包含最终投影结果。

参见 [ADR 0020](./adr/0020-durable-transition-hook.md)、[ADR 0021](./adr/0021-recovery-continuation-api.md) 和
[Enterprise Runtime 持久化参考设计](./enterprise-runtime.zh-CN.md)。
