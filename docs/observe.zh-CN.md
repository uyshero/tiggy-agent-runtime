# 可观测性语义

## 边界

`observe` 把已有 Version 1 Lifecycle Event Stream 转换为日志和指标；它拿不到可变 Agent State，也不改变 Runtime Decision。应用把 Observer Sink 传给 `RunStream` 或 `Continue`；Agent Config 和 `event` 基础协议不导入日志或遥测框架。

Event Sink 仍是同步边界。直接传给 Agent 的 Sink 是严格模式：Error 或 Panic 会成为 Event Sink Failure 并停止投递。`observe.BestEffort` 会隔离 Error 和 Panic，调用可选诊断 Handler 后返回 nil。审计或可靠投递使用严格模式；不能影响 Run 的运行日志和遥测使用 Best Effort。

`observe.Chain` 按顺序调用非 nil Sink。每个 Sink 都收到独立 Event/Payload 副本，因此一个 Observer 不能修改下一个 Observer 的输入。严格模式下首个 Error 会停止 Chain。这些 Helper 不启动 goroutine、不持有 Queue，也无法抢占一个阻塞不返回的 Callback。

`observe.AsyncSink` 是官方提供的有界异步包装器，适合普通运行日志和 Telemetry。它拥有一个 Worker，在入口复制 Event，支持 Queue Size、`drop_newest`、`drop_oldest` 或 `block` 溢出策略，并提供 `Flush(ctx)` 与 `Shutdown(ctx)`，方便应用在优雅关闭时排空已接受 Event。Drop 策略会通过 `OnError` 报告溢出并返回 nil；`block` 会保留背压，Queue 满时仍可能阻塞 Run。AsyncSink 不能抢占已经阻塞的下游 Sink；它只是把投递移到有界 Queue 后面。严格 Audit 和必须完整的 Recording 仍应使用直接同步 Sink。

Queue Size 限制的是缓冲区里的 Event 数量，不是总保留字节数。AsyncSink 不检查或拒绝 Event Payload 大小；上游 Runtime 限制、Recorder 脱敏限制、Provider Adapter 和应用自定义 Sink Policy 仍然负责字节预算。

`AsyncSink.Abort()` 是显式的非等待退出路径。它立即停止接受新 Event，并要求 Worker 放弃 Queue 内事件，但不能抢占已经开始的 Delegate 调用。如果该 Callback 永不返回，对应 Worker goroutine 可能一直存在到进程退出。`Abort` 本身不会等待；之后仍可调用 `Shutdown(ctx)` 等待清理完成。

`NewAsyncBatchSink` 接收一个 `BatchSink`，只聚合 Queue 中已经存在的 Event，最多达到配置的 Batch Size。它不会等待批量 Timer，因此低流量时会立即投递一个 Event 的 Batch。Batch Size 默认 64，受 Queue Size 约束；显式配置大于 Queue Size 会被拒绝。BatchSink Error 或 Panic 只通过 `OnError` 报告一次；因为 Delegate 已被调用，这批 Event 会计入 Processed。如果 BatchSink 在部分写入后返回 Error，Core 无法判断已 Commit 的子集，也不会自动 Retry 整批。

`AsyncSink.Stats()` 返回并发安全、内容无关的快照，包括 Queue/Batch Capacity、Pending、Submitted/Processed Event 数、Delegate Call 数、Newest/Oldest Drop、Abort Discard、Delegate Failure、Rejected，以及 Closed/Aborted 状态。`Processed` 统计已经交给 Delegate 的 Event；`DelegateCalls` 和 `DelegateFailures` 统计 Callback 调用次数，一个多 Event Batch 仍只算一次调用。Queue Depth 只计算缓冲区内 Event；Pending 还包括正在处理的 Event/Batch，以及 `AsyncBlock` 正在等待容量的提交。调用 `Abort` 后，在当前 Delegate 返回、Worker 放弃 Queue 前，Pending 仍可能大于零。快照不会保留 Event、Payload、Error Text、Identifier 或 Timestamp。详细失败仍只通过 `OnError` 短暂交给应用；应用可以把数字快照映射到自己的 Metrics，但不要再写回同一个 AsyncSink。

新增 Event Type 保持前向兼容。LogSink 只记录未知 Event 的 Envelope；Metrics 和 OTel 在定义明确的隐私与基数映射前忽略未知 Payload。

## 结构化日志

`observe.LogSink` 使用 Event Timestamp 写入 `slog.Record`，默认 Level 为：

- Operation Start 和 Delta：Debug；
- 正常 Outcome 与 Completed Run：Info；
- Cancellation、Retry、Fallback 和 Limit：Warn；
- Failed Run、Runtime Error 与 Failed Operation：Error。

Logger 接收 Correlation ID、Event Type/Sequence、有界 Operation Metadata、Token Count 和安全的 `event.ErrorPayload` 字段，但绝不记录原始 Event Payload。尤其不会记录 Model Delta Text/Reasoning、Tool Argument Fragment、Message Content、Artifact Body、Memory Text、Scope Key 或 Provenance ID。Logger 的二次脱敏、输出位置、保留周期和访问控制仍由应用负责。

对于 `model.call.started`，LogSink 会记录选定的固定 Context Manifest 聚合值，例如压缩前/最终 Message 数、Instruction Stack 数量/字节、固定 Instruction Stack 类型字节数、Memory/Compression Projection 数、Text Byte、Artifact Block 和 Tool Definition 数量/字节，但不记录对应正文或 ID。详见 [Context 诊断快照](./context-diagnostics.zh-CN.md)。

## 指标

`observe.Metrics` 消费从 `run.started` 开始的完整 Event Stream，通过 Event Parent ID 关联成对 Operation，并记录：

- Run Count、Duration、Token Usage 与 Cost Micros；
- Model/Tool Call Count 与 Duration；
- Context Prepare/Compression Count 与 Duration；
- Artifact Offload/Memory Operation Count 与 Duration；
- Retry、Fallback 和 Limit Count。

指标使用 Tiggy 自有的 `Measurement`、`Attribute` 和 `Recorder` Contract；Duration 单位是毫秒。一个 Metrics 实例可并发服务多个 Run，并在 Terminal Event 清理 Run/Operation 关联状态，包括异常终止时仍未完成的 Operation。

Metric Attribute 明确排除 Run ID、Event ID、Call ID、Memory Namespace/Scope Key、Error Code 和自由文本 Reason。允许的有界维度包括 Agent Name、已配置 Model Identity、已配置 Tool Name、Operation/Status、Usage Source、Token Type、Context Pressure、Compression Strategy、Tool Error 布尔值和 Error Class。部署仍需限制 Agent、Model、Tool 与 Strategy 配置值的数量。

`observe.NewAsyncMetrics` 把一个 AsyncSink 映射到同一套 Recorder Contract。`Record()` 把 Queue Capacity、有效 Batch Size、Queue Depth、Pending、Closed 和 Aborted 作为 Gauge；Submitted/Processed/Delegate/Drop/Failure/Rejected 等单调累计值则转换为相对上次采样的 Counter Delta。必填且有界的 `observer.name` 用于区分已配置 Sink，不使用 Run/Event ID。AsyncMetrics 不拥有 Timer 或 goroutine，由应用决定采样周期和关闭时的最后一次采样。它会在 Recorder I/O 前推进基线，因此 Recorder 部分失败不会自动补发：宁可丢失该次采样，也不重复已经接受的 Counter Delta。

## OpenTelemetry Adapter

`observe/otel` 使用官方 OpenTelemetry Go API，把 Run 以及配对的 Model、Tool、Context、Compression、Artifact 和 Memory Operation 映射为带 Event Timestamp 和父子关系的 Span，并通过 OTel Instrument 导出同一套 Metrics。Delta Body 不会写入 Span。

构造函数要求应用提供并拥有 `trace.TracerProvider` 和 `metric.MeterProvider`。Adapter 不创建 Exporter、不启动网络 Client、不执行 Flush，也不调用 Provider `Shutdown`。Resource、Sampling、Reader/Exporter、Batch、Timeout 和 Shutdown 顺序全部由应用配置。`Observer.NewAsyncMetrics` 会把 AsyncSink 采样器绑定到同一个 MeterProvider，并把 `MetricGauge` 映射为同步 OTel Gauge。可选且并发安全的 `ParentContext` 能把每个 Run Span 连接到入口 Trace；它的非法结果和 Panic 属于 Observer Failure，可用 `BestEffort` 隔离。

OTel Span Attribute 可以携带 Run/Event Correlation ID 和安全 Error Code，因为 Trace 是诊断记录而非 Metric Dimension；但仍不包含原始 Prompt、Model Delta、Tool Argument/Result、Memory Content、Scope Key 或 Artifact Body。

## 示例

```go
logSink, err := observe.LogSink(logger, observe.LogOptions{})
if err != nil {
    return err
}
telemetry, err := tiggyotel.New(tiggyotel.Options{
    TracerProvider: tracerProvider,
    MeterProvider:  meterProvider,
})
if err != nil {
    return err
}
telemetryAsync, err := observe.NewAsyncSink(telemetry.Sink, observe.AsyncOptions{
    QueueSize:  1024,
    DropPolicy: observe.AsyncDropNewest,
    OnError:    reportObserverError,
})
if err != nil {
    return err
}
asyncMetrics, err := telemetry.NewAsyncMetrics("otel.primary", telemetryAsync)
if err != nil {
    return err
}

sink := observe.Chain(
    observe.BestEffort(logSink, reportObserverError),
    telemetryAsync.Sink,
)
result, err := runtime.RunStream(ctx, input, sink)
if err := telemetryAsync.Shutdown(shutdownCtx); err != nil {
    reportObserverError(err)
    telemetryAsync.Abort()
}
if err := asyncMetrics.Record(); err != nil {
    reportObserverError(err)
}
stats := telemetryAsync.Stats()
```

对于原生支持批量 API 的 Exporter：

```go
telemetryAsync, err := observe.NewAsyncBatchSink(
    func(events []event.Event) error {
        return exporter.Export(events)
    },
    observe.AsyncBatchOptions{
        QueueSize:  1024,
        BatchSize:  64,
        DropPolicy: observe.AsyncDropNewest,
        OnError:    reportObserverError,
    },
)
```

AsyncSink 在 `observe` 包中提供压力与基准覆盖：

```bash
go test ./observe -run 'TestAsyncSinkStressAccounting'
go test ./observe -run '^$' -bench 'Benchmark(DirectSink|AsyncSink|AsyncBatchSink|AsyncSinkParallel|AsyncBatchSinkParallel)$' -benchmem
```

诊断 Error Handler 不能再次写入同一个失败 Sink。Best Effort 只隔离失败，不会把阻塞 Observer 自动变成异步；普通 Telemetry 需要有界排队时使用 `AsyncSink`。
