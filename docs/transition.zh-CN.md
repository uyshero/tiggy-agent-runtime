# Durable Transition Hook

`agent.Config.TransitionHook` 为托管平台提供数据库无关、同步且失败关闭的执行检查点协议。它解决
“只能在 Invocation 返回时拿到完整 `Result.State`”的问题，但不在 Core 中引入数据库、Queue、Lease、
租户或审批身份模型。

Hook 是可选能力。未配置时，`Run`、`Continue` 和 `Resume` 保持原有进程内语义。

## Transition 类型

单次 Runtime Invocation 使用独立的 Run-local `Sequence`，并为每个 Transition 生成稳定 ID：

| 类型 | 持久化边界 |
| --- | --- |
| `model_call.prepared` | Provider Request 已冻结，但尚未调用 Model。 |
| `model_call.completed` | Model 已返回；私有载荷可包含最终 Response。 |
| `tool_batch.frozen` | Tool Call、参数摘要、定义摘要、效果类别和幂等键已冻结。 |
| `tool_effect.prepared` | 单个 Tool effect 已准备，但尚未开始。 |
| `tool_effect.started` | Runtime 即将调用 Tool。崩溃后如果没有终局记录，结果必须视为未知。 |
| `tool_effect.completed` | Tool 已返回确定结果；私有载荷可包含完整 Result。 |
| `tool_effect.indeterminate` | 取消、Runtime Failure 或完成记录失败导致外部结果不确定。 |
| `state_transition.committed` | 候选 State 已通过校验，Hook 成功后 Runtime 才替换内存 State。 |
| `resume_continuation.consumed` | 审批决议已精确校验；Hook 成功后才清除 Suspension。 |

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
- `Private.ToolArguments` / `ToolResult` 保存需要加密的 Tool 恢复材料。

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

## 恢复规则

- 最后记录是 `tool_effect.prepared`：外部效果尚未开始，可以重新调度；
- 最后记录是 `tool_effect.started`：不得自动重放，除非效果类别和外部系统证明可安全处理；
- `tool_effect.completed` 后没有对应 State commit：使用加密保存的 Result 完成 State 提交；
- `tool_effect.indeterminate`：只读/幂等写可按平台策略恢复，可对账写先查询外部引用，不可对账写进入人工处置；
- 重复 Worker 唤醒必须由平台 Lease/Fencing/乐观版本阻止，不能依赖进程内 Hook；
- `resume_continuation.consumed` 的幂等键绑定 Suspension ID，平台必须拒绝第二个有效消费者。

Core 不自动重放 Tool，不提供“人工确认成功后直接伪造 Tool Result”的入口，也不承诺外部效果
exactly-once。平台只有在保存了合法私有 Result 或通过专用补偿/对账流程得到合法结果时，才能推进对应
State。

参见 [ADR 0020](./adr/0020-durable-transition-hook.md) 和
[Enterprise Runtime 持久化参考设计](./enterprise-runtime.zh-CN.md)。
