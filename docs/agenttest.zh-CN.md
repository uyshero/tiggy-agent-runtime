# Agent 测试工具手册

## 用途

`agenttest` 为嵌入 Tiggy Agent Runtime 的应用提供确定性、理解 Agent 协议的 Test Double。它用于 `_test.go`，不会调用真实模型、启动基础设施、修改生产 `agent.Config`，也不能替代 Provider 或数据库 Wire-level Integration Test。

它适合一次验证一条受控执行流程：

```text
预设 Model 响应 -> Runtime 决策 -> Tool 或扩展 -> 下一次 Model Request -> 终止结果
```

完整且参与编译的示例在 [examples/agenttest-basic/agent_test.go](../examples/agenttest-basic/agent_test.go)。运行：

```bash
go test -v ./examples/agenttest-basic
```

## 基本使用流程

1. 用 `ModelStep` 描述每一次预期 Model 调用；
2. 用 Step 的 `Assert` 回调检查准确的 Provider-neutral `model.Request`；
3. 用小型 `Func...` Adapter 构造 Tool 或扩展 Port；
4. 向 `agent.Config` 注入 `SequentialIDs` 和 `StepClock`；
5. 使用 `Trace` Sink 运行 Agent；
6. 断言业务结果、调用次数、稳定错误 Class/Code 和 `ValidateTrace`。

`ScriptedModel` 每次 `Generate` 消费一个 Step。发生额外调用时会返回 `agenttest: unexpected model call N`。`Calls` 返回已复制的 Request，因此 Run 结束后检查时不会共享 Runtime 的可变值。

## 查看传给模型的完整上下文

`ScriptedModel.Calls()` 返回每个 Model Boundary 实际收到的 Provider-neutral `model.Request`。Run 结束后可以打印每次调用真正投影给模型的完整上下文：

```go
for index, request := range provider.Calls() {
    data, err := json.MarshalIndent(request, "", "  ")
    if err != nil {
        t.Fatal(err)
    }
    t.Logf("Model Call #%d:\n%s", index+1, data)
}
```

测试成功时需要使用 `-v` 才会显示日志：

```bash
go test -v -run TestAgentWithScriptedModelAndTool ./examples/agenttest-basic
```

捕获的 Request 包含该次调用实际使用的 System/User/Assistant Message、Tool Call/Result、Recall Memory 投影、Compression 后的 Context View、Tool Definition/Schema、Model Identity、输出上限、Parameters、Run ID 和 Call ID。

也可以在 `ModelStep.Assert` 中同步查看当前 Request：

```go
agenttest.ModelStep{
    Assert: func(request model.Request) error {
        data, err := json.MarshalIndent(request, "", "  ")
        if err != nil {
            return err
        }
        t.Logf("actual model context:\n%s", data)
        return nil
    },
    Response: response,
}
```

下面两份数据含义不同：

```text
provider.Calls()[n].Messages  第 n 次真正发送到 Model Boundary 的 Context View
result.State.Messages         Runtime 保留的完整 Conversation State
```

发生 Compression 时，只会替换第一份数据中的历史视图，完整 State 不变。Recall Memory 也只进入 Request View，不会追加到 State。

这里捕获的是 Tiggy 交给 Provider Adapter 的请求，不能看到 Adapter 内部最终生成的 OpenAI、Anthropic 或其他 Provider-specific HTTP JSON。要检查 Wire Format，应在 Adapter Integration Test 中使用能够捕获 Request 的 Fake HTTP Server 或 Transport。

完整上下文可能包含 Prompt、Tool Result 或 Recall 数据，只应在受控测试中打印，不能把这种做法复制到生产日志。

## Test Double 一览

| API | 用途 |
|---|---|
| `NewScriptedModel` / `ModelStep` | 有序 Model 响应、错误、Delta 和 Request 断言 |
| `FuncModel` | 阻塞、取消、Panic 等自定义 Model 行为 |
| `FuncTool` | 带真实 Definition 和回调的小型原生 Tool |
| `FuncMemory` | Memory Recall/Observe 回调 |
| `FuncTokenCounter` | Provider-specific Token Count 行为 |
| `FuncPlanner` / `FuncCompressor` | Context Planning 和 Compression 回调 |
| `FuncOffloader` | Artifact Offload Decision |
| `SequentialIDs` | `run_1`、`msg_1`、`event_1` 等稳定 ID |
| `StepClock` | 每次调用按固定 Duration 前进的稳定时间 |
| `Trace` | 通过 `Trace.Sink` 并发安全地捕获 Event |
| `ValidateTrace` | 校验生命周期、配对、Sequence、Parent 和 Terminal Invariant |
| `EventTypes` / `DecodePayload` | 不手写 JSON 解析的定向 Event 断言 |

`ScriptedModel.Assert`、`ScriptedModel.Calls`、`FuncMemory`、`FuncTool` 参数以及 Context/Artifact 函数适配器会复制各自拥有的边界值。`FuncModel` 刻意保持为原始 `model.Model` 回调，不额外复制；其实现必须遵守正常的 Model 所有权契约。

## 应该断言什么

有效的 Runtime 测试不能只检查最后一段文字，还应检查：

- 最终 `agent.Status`；失败时检查 `agent.RunError.Class` 和 `Code`；
- Model 和有副作用 Delegate 的调用次数；
- 下一次 Model Request 是否包含预期 Tool Result、Recall Memory 或压缩后的 Context；
- 直接检查 State 时执行 `result.State.ValidateComplete()`；
- 执行 `agenttest.ValidateTrace(result.Events)` 或校验 `Trace` 捕获的 Events；
- 场景涉及 Retry、Compression、Memory 或 Artifact 时，检查对应的强类型 Event Payload。

使用 `DecodePayload[T]` 检查强类型 Payload：

```go
payload, err := agenttest.DecodePayload[event.MemoryPayload](value)
```

除非 ID 或时间就是测试目标，否则不要逐个硬编码 Event ID 和 Timestamp。注入确定性 ID/时间是为了让失败输出可复现，断言重点仍是协议语义。

## 故障注入

`FaultInjector` 可以包装 Context Planner、Compressor、Token Counter 和 Memory Boundary。`FaultRule` 选择精确的、从 1 开始的调用序号，并执行一种 Action：返回 Error、Panic、等待取消、返回非法结果，或 Delegate 成功执行后返回 Error。

测试取消时，应在启动 Run 前注册 `Watch(point, call)`，等待返回的 Channel，再取消 Context。不要用 Sleep 协调取消测试。

每条受控流程使用独立 Injector。Injector 虽然并发安全，但同一 Point 的调用编号取决于进入锁的顺序；多个无关并行 Run 共享 Injector 会让场景归属变得不明确。

Rule 矩阵和代码示例见 [Context 与 Memory 故障注入](./fault-injection.zh-CN.md)。

## 常见场景

| 场景 | 推荐 API | 必须断言 |
|---|---|---|
| 正常回答 | 一个 `ModelStep` | Completed、一次 Model 调用、Trace 合法 |
| Tool 往返 | 两个 `ModelStep` 加 `FuncTool` | 第二次 Request 有 Tool Result，Tool 只调用一次 |
| Provider 故障 | `ModelStep.Err` 配合 `model.ProviderError` | 稳定 RunError Class/Code、Retry 次数、Trace 合法 |
| Model Panic 或取消 | `FuncModel` | Failed/Canceled 和唯一 Terminal Event |
| Memory 降级 | `FuncMemory` 或包装 `memory.Noop` | Policy Outcome 和强类型 Memory Event Payload |
| 有歧义的副作用 | `FaultErrorAfterDelegate` | Delegate 只调用一次且没有隐式重放 |
| Boundary 取消 | `FaultWaitForCancel` 加 `Watch` | 不依赖 Sleep 得到 Canceled |
| 扩展返回非法值 | `FaultInvalidResult` | Fail-closed Error Code 和配对完整的生命周期 Event |

## 能力边界

`agenttest` 验证 Runtime 编排和 Adapter 周围由 Tiggy 拥有的 Contract。它不能验证 OpenAI/Anthropic Wire Mapping、PostgreSQL Transaction、MCP Server 实现、进程崩溃恢复或网络分区。这些需要放在 Adapter Integration Suite 中测试，再由 `agenttest` 验证 Runtime 如何处理 Adapter 归一化后的结果和错误。
