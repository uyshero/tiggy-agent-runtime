# OpenAI-compatible Provider Adapter

## 边界

`provider/openai` 是一个可选、只依赖 Go 标准库的 Adapter，通过流式 Chat Completions 协议实现 `model.Model`。应用必须显式 Import 和构造它；`agent`、`model` 等 Core Package 不依赖该包，Adapter 也不读取环境变量或全局 Credential。

它不是 Tiggy Manage Agent 的 OpenAPI Client，而是在嵌入 Runtime 的当前 Go 进程中直接调用配置的模型端点。

## 构造

应用拥有 API Key、`http.Client`、Transport、连接池、Proxy、Timeout 和 Shutdown：

```go
client := &http.Client{Timeout: 90 * time.Second}
provider, err := openai.New(openai.Config{
    BaseURL: "https://provider.example/v1",
    APIKey:  os.Getenv("MODEL_API_KEY"),
    Client:  client,
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    Name:  "support-agent",
    Model: provider,
    ModelIdentity: model.Identity{
        Provider: "openai-compatible",
        Model:    "configured-model",
    },
    Toolkit: toolkit,
})
```

默认必须使用 HTTPS。`AllowHTTP` 只用于受信任的本地或私有端点。`New` 只校验配置，不发网络请求，也不拥有或关闭注入的 Client。

## 当前映射能力

Adapter 当前支持：

- 文本 System、User 和 Assistant Message；
- Assistant Tool Call 和文本 Tool Result；
- 把 Runtime Tool Definition 映射为 OpenAI Function Tool；
- 流式文本、Reasoning、Tool Call Fragment、Finish Reason 和 Usage；
- 把 `x-request-id` 或 `request-id` 写入成功的 `Response.ProviderRequestID` 或失败的 `ProviderError.RequestID`；
- 把 Provider-neutral `Request.Parameters` 映射为 Wire 顶层字段。

`Request.Parameters` 必须是 JSON Object，且不能覆盖 Runtime 管理的 `model`、`messages`、`tools`、`max_tokens`、`max_completion_tokens`、`n`、`stream` 和 `stream_options`。这会防止 Provider-specific 别名或多 Choice 设置绕过 Runtime 的输出上限和单响应契约。当前不支持 Image、Artifact Block、非文本 Tool Result、旧式非流式 Completions 和 Provider-specific Reasoning Replay；历史中的 Assistant Reasoning Block 会在再次发送时省略。

Tool Call 参数按 Index 拼接。最终参数不是合法 JSON Object 时，Adapter 会产生安全 `{}` 和 `ToolCall.ArgumentsError`，让 Toolkit 返回模型可处理的参数错误，而不是执行畸形参数。

## 故障与流式语义

HTTP 与 Stream 故障会转换成稳定的 `model.ProviderError`，覆盖 Authentication、Permission、Quota、Rate Limit、Context Length、Timeout、Invalid Request、Model Unavailable、Safety Filter、Server、Network、Cancellation 和 Stream Protocol。对非成功 HTTP 响应，Adapter 最多读取 4 KiB，只识别白名单中的结构化 `error.code`/`error.type`，不提取或保留 Provider Message；未知、超限或畸形的 HTTP Error Envelope 继续按 HTTP Status 分类，未知 SSE Error Metadata 则使用安全的 `stream_error` Code。非法 Tool Call Index/Type、冲突的 Finish Reason 和安全过滤终止都会以失败闭合。

Adapter 不自动 Retry。Retry 或 Fallback 必须由 Agent 显式、有界的 `ModelFailurePolicy` 决定。Adapter 原样返回 `DeltaSink` Error 并停止读取，在返回前关闭每个 Response Body，并使用 Run Context 处理取消。单条 SSE Line 上限为 1 MiB；Runtime 另行限制保留的 Delta 数量和字节。

使用 `make eval-provider-reliability` 执行确定性 HTTP/SSE 故障覆盖。`make provider-live` 是显式、可能计费的外部冒烟验收，详见[真实 Provider 契约验收](./live-provider.zh-CN.md)。
