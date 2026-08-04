# Context 与 Memory 故障注入

## 目标与边界

Fault Injection 是 `agenttest` 中的确定性测试基础设施，用于验证 Context/Memory Extension Boundary 故障下的 Agent Failure Classification、Cancellation、Degradation Policy、成对 Lifecycle Event、唯一 Terminal 和禁止自动重放规则。

它不会进入 `agent.Config` 或生产 Package。Core 不会随机让请求失败、不读取环境变量故障开关、不添加真实延迟，也不暴露远程故障控制接口。生产 Chaos Experiment 必须由应用或 Enterprise 在相同 Port 外显式构建。

## 故障点

一个 `FaultInjector` 可以包装：

| Point | 被包装 Contract |
|---|---|
| `context.plan` | `contextmanager.Planner` |
| `context.compress` | `contextmanager.Compressor` |
| `context.token_count` | `model.TokenCounter` |
| `memory.recall` | `memory.Port.Recall` |
| `memory.observe` | `memory.Port.Observe` |

Wrapper 在调用 Delegate 前复制可变 Request，并复制 Memory Recall 返回值；它不拥有也不关闭 Delegate。

## Rule 与 Action

`FaultRule` 精确定位某个 Point 的第 N 次调用。Rule 在构造时一次性校验，之后不可变；没有匹配 Rule 的调用直接进入 Delegate。

- `return_error`：调用 Delegate 前返回带类型的 `FaultError`；
- `panic`：调用前使用配置值 Panic，用于验证 Runtime Panic Boundary；
- `wait_for_cancel`：只等待 `ctx.Done()` 并返回 `ctx.Err()`，不创建 Timer 或 Goroutine；
- `invalid_result`：返回 Point-specific 非法值和 nil Error，用于验证 Invariant；
- `error_after_delegate`：Delegate 成功执行一次后才返回注入 Error，模拟副作用已接受但 Ack 丢失。

非法 Planner/Compressor Result 是零值，非法 Token Count 是 0，非法 Memory Recall 包含跨出请求 Scope 的 Item。Memory Observe 没有 Result，因此该 Point 禁止 `invalid_result`。

`error_after_delegate` 绝不 Retry。对于 Memory Observe，它用于证明 Transport Error 无法判断 Storage Side Effect 是否已经发生。

## 确定性与取消

Call Ordinal 按 Point 单调递增。Injector 本身并发安全，但同 Point 的并发调用顺序由 Mutex 获取顺序决定；需要确定结果时，应为每条受控流程创建独立 Injector，不要让无关并发 Run 共用一个调用序号。

`Watch(point, call)` 返回一个 Channel，在指定调用进入 Injector 时关闭。测试在启动 `wait_for_cancel` 前注册 Watch，收到信号后再 Cancel Context，从而避免 Sleep 和时序竞争。Injector 不创建后台 Goroutine 或 Timer。

## 示例

```go
injector, err := agenttest.NewFaultInjector(agenttest.FaultRule{
    Point:  agenttest.FaultMemoryObserve,
    Call:   1,
    Action: agenttest.FaultErrorAfterDelegate,
    Err:    errors.New("acknowledgement lost"),
})
if err != nil {
    return err
}

faultyMemory, err := injector.WrapMemory(realisticTestMemory)
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    // ... deterministic Model and IDs ...
    Memory:      faultyMemory,
    MemoryScope: testScope,
})
```

测试随后必须断言 `agenttest.ValidateTrace`、Run Status/Error Code、注入调用次数和 Delegate Side-effect Count。只触发故障而不验证 Terminal Invariant 并不充分。

## 非目标

该 Harness 不模拟 Packet-level Corruption、CPU/Memory Pressure、进程终止、Database Failover、Clock Skew、Disk Full、Distributed Partition 或概率延迟。这些需要专门 Adapter 或系统测试环境。它也不会削弱生产 Context/Memory Validation，更不承诺一个合成故障能代表所有 Backend Failure。
