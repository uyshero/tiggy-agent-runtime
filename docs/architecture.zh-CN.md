# 架构与边界

## 产品定义

Tiggy Agent Runtime 是进程内 Go Library。调用方负责构造、生命周期、取消、配置和事件消费。Runtime 可以调用远程模型或 MCP，但不会通过 OpenAPI 调用 Tiggy Manage Agent。

```text
Application
  -> Agent API
     -> Runtime Supervisor
        -> Context Manager
        -> Model
        -> Toolkit
           -> Native Tool
           -> MCP Tool
           -> Skill
           -> Built-in Tool
        -> Memory Manager
        -> Event Stream
           -> Observer
           -> Run Recorder
```

## 依赖方向

```text
agent
  -> message, model, event, tool, toolkit, skill, contextmanager, artifact, memory

observe
  -> event、model 和 Go 标准库

observe/otel
  -> observe、event 和官方 OpenTelemetry Go API

record
  -> event、model 和 Go 标准库

provider/openai
  -> message、model、net/http 和 Go 标准库

toolkit
  -> message, model, tool

skill
  -> safespace 和 Go 标准库

contextmanager
  -> message、model 和 Go 标准库

artifact
  -> message、tool 和 Go 标准库

memory
  -> message 和 Go 标准库

mcp
  -> message、model、tool 和官方 MCP Go SDK

builtin
  -> command, message, model, tool, safespace, permission

command
  -> 仅 Go 标准库

command/local
  -> command、safespace 和 Go 标准库 os/exec

safespace
  -> 仅 Go 标准库

permission
  -> 仅 Go 标准库

approval
  -> 仅 Go 标准库

model, message, event
  -> 仅 Go 标准库

tiggy-manage-agent
  -> tiggy-agent-runtime
```

本仓库禁止反向依赖 `tiggy-manage-agent`。

## Core 职责

- 与 Provider 无关的 Message 和 Event；
- 有边界的 ReAct 执行；
- 模型和工具调用限制；
- 取消传播和资源清理；
- Toolkit、MCP、Skills 和内置工具；
- Root 约束的 SafeSpace 和可注入 FileSystem 接口；
- 可注入的命令执行 Port 和显式选择的 Adapter；
- Context Token 规划和语义安全压缩；
- 通过注入 Store Port 实现大型 Tool Result Artifact Offload；
- Memory Recall、Observe 和质量策略；
- 结构化异常、Retry 和 Fallback；
- 进程内 Tool 人工审批暂停与精确调用恢复；
- 日志、指标、Trace 和 Run Recorder；
- 确定性测试和故障注入。

## 不属于 Core

- HTTP、SSE 和 OpenAPI 服务端；
- 用户、租户、RBAC 和数据库 Workspace；
- PostgreSQL Schema 和迁移；
- Worker Queue、Lease 和分布式调度；
- 数据库级 Durable Agent State Machine；
- 企业审批身份、持久化、协调、审计导出和计费；
- 生成式 OpenAPI Client SDK。

## Runtime 不变量

1. 每次 Run 恰好产生一个终止事件。
2. 每个已接受 Tool Call 都有一个终局结果、明确中断或经过校验的审批暂停。
3. Tool Result 引用真实 Tool Call，且不能重复终局。
4. 取消传播到 Model、MCP、Tool、Memory 和 FileSystem。
5. 迭代、模型调用、工具调用、Token、流式 Delta 数量/字节、输出和 Deadline 均有边界。
6. 并发事件可以交错，但 Tool Result 按模型调用顺序投影。
7. Context Compression 保持 Tool Call/Result 和未完成工作完整。
8. 扩展失败必须分类，不能静默破坏 Runtime 状态。
9. 每个 goroutine 和资源都有明确的停止与释放路径。
10. Event Sequence 和父子关联稳定、可测试。

这些是进程内保证。第一版不承诺进程退出后的自动恢复和外部副作用 exactly-once。

当前 P1 使用顺序 Tool 执行和同步 Event Sink。`Result.Events` 是完整内部诊断轨迹；Sink 在终止事件前失败会中止 Run，只有终止事件投递失败时保留真实执行状态并单独返回投递错误。详细语义见 [Agent Runtime 语义](./runtime.zh-CN.md)。

`observe` 提供只读 Sink 组合、结构化 `slog` Metadata 和有界基数 Metric Derivation。`observe/otel` 把相同 Event 映射到应用拥有的 OpenTelemetry Provider。严格 Sink 保留 Delivery Failure 语义；当 Monitoring 不能影响执行时，`BestEffort` 显式隔离运行 Observer 的 Error 和 Panic。两个包都不接收 Agent State、不拥有后台 Exporter，也不记录原始 Event Payload。详见 [可观测性语义](./observe.zh-CN.md)。

`record` 把同一 Event Stream 增量转换为带版本、隐私感知且按 Run 独立的 SHA-256 Chain。它校验通用 Sequence/Parent/Terminal Invariant，并提供有界 Memory Journal 和调用方拥有的 JSONL Journal。Partial Journal 是诊断证据，不是可恢复执行 State；Digest 用于发现损坏，不是 Signature。详见 [Run Recorder 语义](./record.zh-CN.md)。

`agenttest.FaultInjector` 使用精确调用序号包装 Context Planner/Compressor/Token Counter 和 Memory Recall/Observe Boundary。它只用于测试，不拥有 Delegate 资源、不启动 Timer/Goroutine，并在不增加生产 Fault Control 的情况下验证 Error、Panic、Cancellation、Invalid Result 和 After-delegate Ambiguity。详见 [Context 与 Memory 故障注入](./fault-injection.zh-CN.md)。

## 扩展权限

```text
Port        替换外部能力：Model、Tool、Memory、FileSystem、Command Runner
Policy      作出有边界决策：Retry、Compression、Permission、Scheduling
Middleware  包裹操作：Tracing、Logging、Cache
Observer    只读消费 Event，不改变 Runtime 决策
```

Middleware 不获得无限制可变 Agent State；Policy 返回强类型 Decision；Port 只拥有自己创建的资源。

`command.Runner` 是 `builtin.bash` 使用的 Core Port，Core 不默认选择执行后端。可选 `command/local` Adapter 使用 `os/exec`；它的 Root 只约束初始工作目录，不是文件系统、网络或进程沙箱。需要隔离的应用必须注入 Container、VM、Remote Executor 或其他专用 Runner。

`mcp.Client` 拥有显式连接的 Session，并把发现的 Remote Tool 投影到原生 Tool 契约。官方 SDK 类型保持私有。Reconnect 会替换 Session，但绝不重放 Tool Call，因为 Transport 失败后的外部效果具有歧义。

`skill.Catalog` 通过由调用方拥有的只读 SafeSpace 契约发现严格版本化 Manifest。应用在构造 Agent 前显式激活可信指令、选定 Resource 和已声明 Toolkit Group。Skill 不执行代码、不拉取包，也不动态获得权限。

## Context、Memory 和 Artifact

```text
Context   下一次模型调用看到的内容
Memory    跨 Run 或长对话保留的信息
Artifact  不直接放入模型上下文的大型产物
```

Context 规划必须计算 System Prompt、Tool Schema、输出预留、未完成 Tool 关系、近期消息、Recall Memory 和模型窗口。Compression 按语义组工作，不按任意字符区间截断。

`contextmanager.TokenPlanner` 通过注入的 Provider-aware 或显式 Estimated Counter 执行不可变预检。发生 Overflow 时，可选的 Model-specific `Compressor` 只接收 Core 构造的非活跃语义组。Core 保留 System Message、当前轮、未完成 Tool Call 和 Tool Call/Result 关系，自行构造摘要替换，并在 Provider Call 前重新规划一次。显式 Failure Policy 还可在 Fits 预检后 Provider 仍报 Context Length 时复用该边界一次，用于容纳 Tokenizer 或 Endpoint 漂移，但不伪造 Overflow 数值。Compression 只改变 Request View，不改写完整 Agent State。失败或压缩不足时采用 Fail-closed，而不是有损删除。

这些保证是结构保证。Core 记录 Strategy/Version 和来源 Message ID，但无法证明注入摘要的事实完全忠实。Compressor 质量及其拥有的外部资源由应用负责。可选 `evaluation/compression` 包帮助应用建立来源绑定事实、大小、Provenance 和压缩后预算 Fixture，但不会成为 Runtime 执行依赖。详见 [ADR 0007](./adr/0007-context-compression.md) 和 [ADR 0016](./adr/0016-compressor-quality-evaluation.md)。

新 Runtime 的每次物理 Model Call 还会在 Start Event 上记录固定大小、无正文的最终 Context Manifest。它把 Request 形态诊断与 Call ID 关联起来，但不会向 Observer 提供 Agent State 或 Request Body。详见 [Context 诊断快照](./context-diagnostics.zh-CN.md) 和 [ADR 0017](./adr/0017-model-context-diagnostic-manifest.md)。

大型成功 Tool Result 可在写入 State 和投影给模型前经过可选 `artifact.Offloader`。Core 把完整 Result 编码为版本化 Envelope，以 SHA-256 绑定，并只接受 Media Type、Size 和 Digest 匹配的 opaque Reference。注入 Store 负责 Durability、Authorization、Encryption、Retention 和 Garbage Collection。Core 不把 Signed URL 放入 Message/Event，不重试 Put，也不自动把 Artifact 取回 Context。详见 [Artifact Offload 语义](./artifact.zh-CN.md)。

注入的 `memory.Port` 在 Model Execution 前执行一次 Scoped Recall，在成功 Response Commit 后执行一次 Observe。Recall 只改变 Request View，Observe 只接收当前 Turn。普通失败默认降级，Cancellation 和非法/跨 Scope Result 始终 Fatal。Core 提供边界和 Event，不提供 Durable Storage 实现。详见 [Memory Recall 与 Observe 语义](./memory.zh-CN.md)。

Core 提供 Noop 和有界进程内 InMemory Reference Port。InMemory 只在显式 Admission 后才从 Observe 写入 Item，不提供 Durability；生产存储仍是注入 Adapter 的责任。Exact Scope、Inclusive Expiry、Source Correlation 和显式 ID Conflict 是结构规则。Agent 在模型投影前再次校验 Expiry，Semantic Truth 和物理删除仍不属于 Core。

Confidence、Semantic Deduplication/Merge、Contradiction Handling、物理删除和 Fact Admission Decision 仍是应用的显式 Policy，不是存储后端的隐式行为。
