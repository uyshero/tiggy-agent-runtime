# Runtime 可靠性评测

## 目标

可靠性基线把 Runtime 的设计承诺变成可执行门禁。它只通过公开的 `agent`、`agenttest`、`contextmanager`、`memory` 和 `toolkit` API 运行确定性场景，并检查返回 State、错误分类、Lifecycle Trace、诊断证据和底层 Delegate 调用次数。

它回答一个收敛的问题：已知故障发生后，Runtime 能否以可预测方式结束、保持合法 State、用稳定证据说明原因，并且不发生未声明的自动重放。

## 如何运行

基线不需要真实模型凭证、数据库、网络或 Enterprise 服务：

```bash
make eval-reliability
```

命令生成：

- `reports/reliability-baseline.json`：供 CI 解析和后续对比；
- `reports/reliability-baseline.md`：供人工审阅。

生成报告默认不进入 Git；需要留存时应作为 CI Artifact 上传。`make verify` 也会运行该基线。全部场景通过时退出码为 `0`，不变量回归时为 `1`，报告编码或写入失败时为 `2`。

也可以直接指定输出路径：

```bash
go run ./cmd/tiggy-runtime-eval \
  -json /tmp/tiggy-reliability.json \
  -markdown /tmp/tiggy-reliability.md
```

## 当前场景

| 场景 | 预期结果 | 主要证明 |
|---|---|---|
| 正常完成 | `completed` | 正常控制路径保持合法 |
| Model Panic | `failed model/model_failed` | Provider Panic 被边界隔离 |
| 非法 Model Response | `failed model/invalid_model_response` | 非法输出不会进入 State |
| Model Delta 超限 | `failed limit/max_model_deltas` | 流式输入有明确边界 |
| Context Overflow | `failed limit/context_overflow` | 上下文溢出时不会调用模型 |
| Provider Context Length 恢复 | `completed` | Policy 允许的一次压缩重试使用缩减视图 |
| Provider Context Length 重复失败 | `failed model/context_length` | 恢复最多压缩一次、调用 Provider 两次 |
| Compressor Panic | `failed internal/context_compression_failed` | 压缩扩展 Panic 被隔离 |
| Memory Recall 失败 | `failed memory/memory_recall_failed` | Fail-run Policy 有稳定分类和证据 |
| Tool Panic | `failed tool_runtime/tool_runtime_failed` | 内部错误细节不会进入模型处理路径 |
| Tool 取消 | `canceled canceled/tool_canceled` | 取消会闭合 Tool 生命周期和 Run |
| 非法 Tool Result | `failed tool_runtime/tool_runtime_failed` | 扩展输出在提交 State 前经过校验 |
| Event Sink 失败 | `failed event_sink/event_sink_failed` | 内部 Trace 仍能形成唯一且闭合的终态 |

## 检查的不变量

每个场景必须同时通过五项检查：

1. `terminal_invariant`：`agenttest.ValidateTrace` 接受 Trace，包括连续 Sequence、成对操作事件和唯一 Terminal Event。
2. `state_invariant`：返回 State 通过 `ValidateComplete`；挂起 Run 则通过 `ValidateSuspended`。
3. `classification`：Status 与 `RunError` Class/Code 必须精确匹配声明值。
4. `diagnostics`：必需 Lifecycle Event 存在，`runtime.error` 携带同一组安全 Class/Code。
5. `no_implicit_replay`：Model、Tool、Memory、Compressor、Token Counter 和 Sink 的 Delegate 调用不得超过场景声明上限。

报告 Schema 固定为 `tiggy.runtime.reliability.v1`。修改不兼容结构时必须升级版本；只允许向后兼容地新增字段。场景输入、ID、时钟、故障和预期结果是确定的；`generated_at`、`duration_micros`、Goroutine 调度和最终文件字节不属于性能承诺，允许每次运行不同。

## 新增场景的标准

只有引入新的故障边界或执行不变量时才新增场景。场景必须只用公开 Runtime API 和确定性 Test Double，并显式声明 Status、错误 Class/Code、必需诊断 Event 和最大 Delegate 调用次数。禁止依赖真实 Provider、网络、数据库、基于 Sleep 的时序或 Enterprise 类型。

这套基线不是延迟 Benchmark、压力测试、模型答案质量评测、数据库 Durability 测试、进程崩溃恢复测试或分布式 Chaos 环境。这些属于独立评测层，不应把快速本地门禁扩张成一套过重系统。

Provider HTTP/SSE 分类属于独立的确定性评测层，通过 `make eval-provider-reliability` 运行。其夹具覆盖成功、HTTP 认证/限流/Quota/Context Length/不可用、畸形或中断 Stream、不完整 Stream、安全过滤、Request Deadline、Delta Backpressure 和失败 Request ID 传递。显式计费的外部冒烟验收仍使用 `make provider-live`。详见[真实 Provider 契约验收](./live-provider.zh-CN.md)。
