# Tiggy Agent Runtime 快速开始与集成手册

## 适用范围

本手册给出从离线示例到真实应用集成的完整路径。Tiggy Agent Runtime 是嵌入 Go 进程的 Runtime Library，不是 OpenAPI Client，也不自动创建模型账号、数据库、MCP Server、Telemetry Backend 或 Worker。

最小运行只需要：

```text
一个 model.Model + 一个 agent.Config + 一条 User Message
```

Toolkit、Memory、Context Planner、MCP、Skills、Observer 和 Recorder 都是按需增加的能力。

当前要求 Go 1.25.12 或更高版本。项目采用 Apache-2.0 许可证，在 Owner 批准并发布 Tag 前仍处于预发布阶段。

## 1. 先运行离线示例

在仓库根目录执行：

```bash
make verify
go run ./examples/basic-agent
```

该示例不读取模型凭证，也不访问网络。它会运行下面的完整流程：

```text
User Message
    -> demo Model 请求 current_time Tool
    -> Runtime 校验并执行 Tool
    -> Tool Result 进入下一次 Model Request
    -> Model 返回最终答案
    -> Run Completed
```

实现见 [examples/basic-agent/main.go](../examples/basic-agent/main.go)。真实应用只需要把其中的 `demoModel` 替换为实现 `model.Model` 的 Provider Adapter。

## 2. 理解最小对象

| 对象 | 责任 |
|---|---|
| `model.Model` | 把 Provider-neutral Request 转换为具体模型调用，再返回统一 Response/Error |
| `tool.Tool` | 声明 Tool Definition，并执行一次有界操作 |
| `toolkit.Toolkit` | 注册 Tool，在每次 Run 开始时生成不可变快照 |
| `agent.Agent` | 执行有界 ReAct Loop，维护 State 并产生 Event |
| `message.Message` | 表示 System、User、Assistant 和 Tool 消息 |
| `event.Sink` | 同步消费一次 Run 的生命周期事件 |

Core 不拥有 Provider Credential、数据库连接、MCP Client、SafeSpace、Telemetry Provider 或其他注入资源。创建它们的应用也负责关闭它们。

## 3. 实现 Model Provider Adapter

Runtime 通过一个小接口调用模型：

```go
type Model interface {
    Generate(
        context.Context,
        model.Request,
        model.DeltaSink,
    ) (model.Response, error)
}
```

Adapter 的工作顺序通常是：

1. 从 Adapter 自己的配置读取 Credential 和 HTTP Client；
2. 把 `request.Messages`、`request.Tools`、`request.MaxOutputTokens` 和 `request.Parameters` 映射到 Provider Wire Format；
3. 把流式片段按顺序传给可选 `sink`；
4. 把最终消息、Stop Reason 和 Usage 转成 `model.Response`；
5. 把 Provider 失败转成 `model.ProviderError`，明确 Class、Retryable 和安全信息；
6. 响应 `ctx` 取消，并在返回前释放 Response Body 或 Stream。

结构骨架如下，其中 `callProvider` 和转换函数由具体 Adapter 实现：

```go
type Provider struct {
    client *http.Client
    token  string
}

func (p *Provider) Generate(
    ctx context.Context,
    request model.Request,
    sink model.DeltaSink,
) (model.Response, error) {
    wireRequest, err := toWireRequest(request)
    if err != nil {
        return model.Response{}, err
    }

    wireResponse, err := p.callProvider(ctx, wireRequest, sink)
    if err != nil {
        return model.Response{}, mapProviderError(err)
    }
    return fromWireResponse(wireResponse)
}
```

这是 Adapter 结构说明，不是可直接运行的内置 Provider。Core 当前不绑定 OpenAI、Anthropic 或其他厂商 SDK。

`model.Identity` 只记录用于诊断和路由的 Provider/Model/Revision，不得放入 Credential。Adapter 也不能把原始 Provider Error Body 直接放入模型可见错误、Event 或默认日志。

## 4. 注册原生 Tool

一个原生 Tool 实现 Definition 和 Execute：

```go
type lookupTool struct{}

func (lookupTool) Definition() model.ToolDefinition {
    return model.ToolDefinition{
        Name:        "lookup",
        Description: "Look up one record by ID.",
        InputSchema: json.RawMessage(`{
            "type":"object",
            "properties":{"id":{"type":"string"}},
            "required":["id"],
            "additionalProperties":false
        }`),
    }
}

func (lookupTool) Execute(
    ctx context.Context,
    arguments json.RawMessage,
) (tool.Result, error) {
    // Validate business identity/authorization in the application layer.
    return tool.TextResult("record found"), nil
}
```

把 Tool 注册进 Toolkit：

```go
kit, err := toolkit.New(lookupTool{})
if err != nil {
    return err
}
```

需要 Namespace 和能力分组时：

```go
err := kit.RegisterWith(toolkit.Registration{
    Tool:      anotherTool,
    Namespace: "crm",
    Groups:    []string{"read", "customer"},
})
```

Agent 可以通过 `Config.ToolGroups` 只暴露指定 Group。Tool 输入在执行前按 JSON Schema 校验。模型可修正错误返回 `tool.Error`；普通 error、Panic 或非法 Result 会作为 Tool Runtime Failure 终止 Run。

## 5. 构造 Agent

最小配置：

```go
runtime, err := agent.New(agent.Config{
    Name:          "support-agent",
    SystemPrompt:  "Use tools when evidence is required.",
    Model:         provider,
    ModelIdentity: model.Identity{
        Provider: "company",
        Model:    "stable",
    },
    Toolkit: kit,
    Limits:  agent.DefaultLimits(),
})
if err != nil {
    return err
}
```

`agent.New` 会立即校验配置，并保存不可变配置副本。默认 Limits 只是通用上限；实际应用应根据场景收紧迭代、Model Call、Tool Call、Token 和总时长。

同一个 Agent 可以并发复用，但所有注入的 Model、Tool、Memory、Planner、Compressor 和 Policy 也必须满足对应的并发契约。

## 6. 运行并消费 Event

创建 User Message：

```go
input := message.Message{
    Role:       message.RoleUser,
    Visibility: message.VisibilityPublic,
    Content: []message.Content{{
        Type: message.ContentText,
        Text: "查询客户状态",
    }},
}
```

不需要实时 Event 时使用 `Run`：

```go
result, err := runtime.Run(ctx, input)
```

需要日志、Telemetry、Recorder 或 UI Timeline 时使用 `RunStream`：

```go
logSink, err := observe.LogSink(slog.Default(), observe.LogOptions{})
if err != nil {
    return err
}

sink := observe.BestEffort(logSink, func(err error) {
    fmt.Fprintf(os.Stderr, "observer error: %v\n", err)
})
result, err := runtime.RunStream(ctx, input, sink)
```

直接传入的 Sink 是严格边界：返回 error 或 Panic 会使 Run 失败。Logging 和 Telemetry 通常用 `observe.BestEffort`；不能丢失的 Audit/Recording 使用严格 Sink。

始终同时检查 `result` 和 `err`。`result.Status` 是 Completed、Failed 或 Canceled；`err` 还可能表示终止事件未成功交付。

## 7. 继续对话

`Run` 从空 State 开始。继续一次已经完整结束的会话时：

```go
next, err := runtime.Continue(ctx, result.State, nextInput, sink)
```

`Continue` 会复制传入 State，并拒绝带未完成 Tool Call 的非法状态。`Result.State` 是进程内值，不是数据库 Snapshot、跨进程 Checkpoint 或崩溃恢复协议。需要保存会话时，由应用定义存储、版本、授权和迁移策略。

## 8. 增加 Memory

先定义应用级 Scope：

```go
scope := memory.Scope{
    Namespace: "workspace",
    Key:       "user-123",
}
```

只想显式启用 Memory 生命周期但不存数据时，可以使用 `memory.Noop{}`。本地开发也可以创建有界、非持久化的 InMemory Port：

```go
mem, err := memory.NewInMemory(memory.InMemoryOptions{
    InitialItems: []memory.Item{{
        ID:    "preference-language",
        Scope: scope,
        Text:  "The user prefers Chinese responses.",
        Provenance: memory.Provenance{
            Source: "application-seed",
        },
    }},
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    // Model, ModelIdentity, Toolkit, Limits ...
    Memory:        mem,
    MemoryScope:   scope,
    MemoryOptions: memory.DefaultOptions(),
})
```

没有配置 Admission 时，InMemory Observe 不会写入新 Item。需要学习当前 Turn 时，应用必须提供 `memory.Admission`，明确哪些内容可以进入 Memory，并生成指向当前 Run/Message 的 Provenance。

企业数据库实现 `memory.Port` 即可。数据库 Record、Tenant ID、Embedding 和 Vector Client 保持在 Adapter 内，不进入 Core API。

## 9. 接入 MCP Tool

MCP 是可选外部资源。应用先创建并连接 Client，再把发现的远程 Tool 注册进 Toolkit：

```go
connector, err := mcp.NewStdioConnector(mcp.StdioOptions{
    Implementation: mcp.Implementation{
        Name:    "my-app",
        Version: "1.0.0",
    },
    Path:        "/absolute/path/to/mcp-server",
    Directory:   "/absolute/path/to/workspace",
    Environment: []string{"API_TOKEN=" + token},
})
if err != nil {
    return err
}

client, err := mcp.NewClient(mcp.ClientOptions{Connector: connector})
if err != nil {
    return err
}
if err := client.Connect(ctx); err != nil {
    return err
}
defer client.Close()

for _, remote := range client.Tools() {
    if err := kit.RegisterWith(toolkit.Registration{
        Tool:      remote,
        Namespace: "remote",
        Groups:    []string{"mcp"},
    }); err != nil {
        return err
    }
}
```

先完成 MCP 注册，再调用 `agent.New`。Client 生命周期属于应用。stdio 的 nil Environment 表示空环境，不继承 Host Secret；MCP Process 也不是安全沙箱。Transport 失败后 Runtime 不会自动重放可能已经产生副作用的 Tool Call。

Streamable HTTP、Reconnect、Health 和 Wire Limit 见 [MCP Client 语义](./mcp.zh-CN.md)。

## 10. 激活 Skills

Skill 是应用预先选择的可信 System Instruction 资源，不会自动执行代码或自动获取 Tool。

```go
space, err := safespace.OpenSafeSpace(
    "./agent-assets",
    safespace.DefaultLimits(),
)
if err != nil {
    return err
}
defer space.Close()

catalog, err := skill.Discover(
    ctx,
    space,
    "skills",
    skill.DefaultLimits(),
)
if err != nil {
    return err
}

active, err := catalog.Activate(ctx, skill.ActivationRequest{
    Name:      "customer-support",
    Resources: []string{"policy"},
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    // Model, ModelIdentity, Toolkit, Limits ...
    Skills: active,
})
```

示例假设 `./agent-assets/skills/<directory>/skill.json` 存在。Manifest 格式、Resource 规则和 Trust Boundary 见 [Skill 语义](./skill.zh-CN.md)。Skill 声明的 Tool Group 必须已经存在于 Toolkit。

## 11. 配置 Context Planning

真实模型集成建议显式配置 Provider-aware Token Counter：

```go
planner, err := contextmanager.NewTokenPlanner(
    contextmanager.TokenPlannerOptions{
        Counter:             providerCounter,
        CountSource:         contextmanager.CountSourceProvider,
        ContextWindowTokens: 128_000,
        SafetyMarginTokens:  1_024,
    },
)
if err != nil {
    return err
}

config.ContextPlanner = planner
```

Core 不根据 Model Name 猜测 Tokenizer 或 Context Window。发生 Overflow 时，没有 Compressor 会在 Provider Call 前 Fail-closed；配置 Compressor 后，Runtime 只允许它摘要可压缩的历史语义组，并保留完整 State。

Compressor 通常可能再次调用模型，因此由应用负责它的 Credential、成本、取消和并发安全。详见 [Context Manager 语义](./contextmanager.zh-CN.md)。

## 12. 增加 Run Recorder

本地诊断可以使用进程内 Journal：

```go
journal, err := record.NewMemoryJournal(record.MemoryOptions{})
if err != nil {
    return err
}
recorder, err := record.New(journal, record.Options{})
if err != nil {
    return err
}

sink := observe.Chain(
    observe.BestEffort(logSink, reportObserverError),
    recorder.Sink,
)
result, err := runtime.RunStream(ctx, input, sink)
```

这里 Recorder 是严格 Sink：记录失败会使 Run 失败。`MemoryJournal` 在进程退出时丢失；JSONL 的 Writer、Flush、fsync、Close、Rotation 和 Retention 由应用负责。Recorder 用于诊断和完整性检查，不提供执行 Replay 或数字签名。

## 13. 编写确定性测试

运行已经提供的 Model-Tool-Model 示例：

```bash
go test -v ./examples/agenttest-basic
```

测试结构：

```go
provider := agenttest.NewScriptedModel(
    agenttest.ModelStep{Response: firstResponse},
    agenttest.ModelStep{Response: finalResponse},
)

runtime, err := agent.New(agent.Config{
    Name:          "test-agent",
    Model:         provider,
    ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
    IDs:           agenttest.SequentialIDs(),
})
```

测试应同时断言：

- Result Status 和稳定 RunError Class/Code；
- Model、Tool 和其他有副作用 Delegate 的调用次数；
- `provider.Calls()` 中真正发送给模型的 Context View；
- `agenttest.ValidateTrace(result.Events)`；
- Cancellation、Panic、Invalid Result 和 After-delegate Failure。

完整说明见 [Agent 测试工具手册](./agenttest.zh-CN.md)和[故障注入手册](./fault-injection.zh-CN.md)。

## 14. 推荐集成顺序

不要一次启用全部能力。推荐顺序是：

```text
离线示例
  -> 真实 Provider Adapter
  -> 一个原生 Tool
  -> RunStream + 结构化日志
  -> agenttest 异常路径
  -> Context Planner
  -> Memory 或 MCP
  -> Skills、Recorder、OpenTelemetry
```

每增加一个外部边界，都先明确 Context 取消、Timeout、资源 Close、错误映射、敏感信息和副作用重放策略。

## 上线前检查

- 为 Agent 设置场景化 Limits，不直接依赖宽松默认值；
- Provider Adapter 响应取消并返回类型化 `ProviderError`；
- 配置真实 Token Counter 和正确 Context Window；
- Tool 在外部效果前完成授权，且不依赖 Runtime 自动重试；
- Memory 使用稳定 Scope，并测试跨 Scope 和过期数据；
- MCP/SafeSpace/Telemetry/Journal 等资源有明确 Owner 和 Close 顺序；
- Logging/Telemetry 使用合适的 Best-effort Boundary，Audit 使用严格边界；
- 默认日志不输出 Prompt、Tool 参数、Memory 内容和 Credential；
- 使用 `agenttest` 覆盖 Timeout、Cancellation、Panic 和有歧义副作用；
- 运行 `make verify`、`make vuln`，并根据实际 MCP 使用情况运行 `make mcp-compat`。

这些检查建立进程内可靠性基线，不代表跨进程恢复、数据库 Durability 或 Exactly-once 外部效果。

## 深入阅读

- [架构与边界](./architecture.zh-CN.md)
- [Agent Runtime 语义](./runtime.zh-CN.md)
- [Toolkit 语义](./toolkit.zh-CN.md)
- [可观测性语义](./observe.zh-CN.md)
- [Memory 语义](./memory.zh-CN.md)
- [Context Manager 语义](./contextmanager.zh-CN.md)
- [MCP Client 语义](./mcp.zh-CN.md)
- [Skill 语义](./skill.zh-CN.md)
- [Run Recorder 语义](./record.zh-CN.md)
- [Agent 测试工具手册](./agenttest.zh-CN.md)
