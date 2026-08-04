# MCP Client 语义

## 范围与构造

`mcp` 包把远程 MCP Tool 适配为原生 `tool.Tool`。当前支持 Tool 发现、Tool Call、Ping 健康检查、显式重连、stdio 和 Streamable HTTP。Prompt、一等 Resource API、Sampling、Roots、Elicitation 和自动 Multi-round-trip Input 暂未启用。

```go
connector, err := mcp.NewStdioConnector(mcp.StdioOptions{
    Implementation: mcp.Implementation{Name: "my-app", Version: "1.0.0"},
    Path:            "/absolute/path/to/mcp-server",
    Directory:       "/absolute/path/to/workspace",
    Environment:     []string{"API_TOKEN=" + token},
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
        Tool: remote, Namespace: "remote", Groups: []string{"mcp"},
    }); err != nil {
        return err
    }
}
```

官方 MCP Go SDK 只是私有实现细节，公开契约仅使用 Tiggy 值和 Go 标准库配置。决策详见 [ADR 0004](./adr/0004-official-mcp-go-sdk.md)。

## 生命周期

`NewClient` 创建未连接 Client。`Connect` 协商一个 Session，并在发布任何 Tool 前完成有界分页发现。连接失败或 Catalog 校验失败会关闭候选 Session，Client 保持未连接。`Health` 发送 Ping。`Close` 等待正在使用当前 Session 的调用、关闭 Session，然后永久关闭 Client；它是幂等的。

`Reconnect` 先连接并发现替代 Session，再完成交换。它不重试或重放进行中/失败的 Tool Call。已注册 Tool Adapter 会按原 Remote Name 调用替代 Session，但新增和删除只会出现在新的 `Tools` Snapshot 中。Catalog 变化很重要时，应用应显式重建 Toolkit 注册。

本实现不承诺 Exactly-once。取消或 Transport 失败可能发生在 Server 产生外部效果之后、Result 到达之前，因此禁止自动重放 Tool Call。

## 限制

字段为零时使用以下默认值：

```text
Tool Catalog             256 Tools
发现分页                  32 Pages
Catalog Schema/Text        2 MiB
Tool Arguments           256 KiB
Tool Result              128 KiB
Result Content Item        64
Tool Call Timeout          60s
Wire JSON/SSE Event         4 MiB
```

Remote Tool 默认顺序执行。只有 Server 和底层资源允许调用重叠时，应用才能显式选择 `model.ToolConcurrencySafe`。Toolkit 仍会独立编译每个导入的 Input Schema，并应用 Namespace 和 Group 规则。

## Transport

Stdio 要求绝对可执行文件和已存在的绝对工作目录，直接启动进程而不经过 Shell。nil Environment 表示没有环境变量，不会继承 Host Secret；Argument 和 Environment 都会被复制。应用可注入 Stderr Writer 用于受控诊断。关闭时先关闭协议 I/O，再中断并最终在有界等待后强制终止无响应进程。这是进程生命周期管理，不是沙箱；Server 仍拥有 Runtime 进程的 Host 权限。

Streamable HTTP 对非 Loopback Endpoint 默认要求 HTTPS，除非显式设置 `AllowInsecureHTTP`。URL Userinfo 和 Fragment 被禁止。自定义 Header 会被复制，且不能覆盖 MCP 协议 Header。Standalone SSE 默认关闭，必须显式启用；SDK 对已启用 SSE 通知流的重连不会重放 Tool Call。调用方拥有注入的 `http.Client` 及其 Transport 资源。

## Result 与失败

MCP Text 转为 Text Content，Image 转为有界 Image Data URL，Resource Link 和文本 Embedded Resource 转为 Text，Embedded Image 转为 Image Content。Audio 和其他 Binary Resource 返回 `unsupported_mcp_content`，因为当前 Message 协议无法安全表示，实现不会静默丢弃。

Structured Content 保留在 Tool Result State 中，且在它是唯一结果时转为 Text。空的成功 Result 会获得非空完成摘要。MCP `isError` Result 转为模型可处理的 `mcp_tool_error`；Timeout、Argument/Output Limit 和不支持 Content 也是模型可处理错误，且永远不自动重试。

Connection、Discovery、Ping、Protocol、非法 Response、Session State 和扩展 panic 使用稳定 RuntimeError Code。Context 取消直接传播。忽略 Context 的 Session 实现仍无法被 Core 强制抢占。

## 外部兼容性

默认测试保持确定性，不需要 Node.js 或网络。`make mcp-compat` 是显式运行的黑盒 stdio 套件，对立实现的官方 TypeScript 参考 Server 进行验证：

| 参考 Server | 锁定版本 | 验证契约 |
|---|---:|---|
| `@modelcontextprotocol/server-everything` | `2026.7.4` | Initialize、Tool Discovery、Ping、`echo`、Text Result、Shutdown |
| `@modelcontextprotocol/server-filesystem` | `2026.7.10` | Initialize、Tool Discovery、Ping、限定目录的 `read_text_file`、Structured Result、Shutdown |

该 Target 需要类 Unix Host、Node.js、`npx`，且在本地没有固定包缓存时需要访问 npm Registry。测试使用临时 npm Cache，只向 Server 进程传递显式的最小环境。缺少 `npx` 会明确报告为 Skip，不影响默认 `make verify`。独立的 GitHub Actions Workflow 使用 Node.js 22，支持手动触发并每周运行一次该网络依赖矩阵。

该矩阵只证明列出版本和已覆盖契约的 stdio 互操作性，不代表认证所有 MCP 能力或任意第三方 Server。Streamable HTTP 当前由官方 Go SDK 兼容测试和本地 Wire Limit 测试覆盖；有稳定、非交互式的独立参考 Fixture 后再扩展外部 HTTP 矩阵。
