# 真实 Provider 契约验收

真实 Provider 验收用于证明一个 OpenAI-compatible 端点能够通过可选 `provider/openai` Adapter 和完整 Agent Runtime 满足 Tiggy 的 `model.Model` 契约。它是显式启用的集成测试，不属于普通 CI 流量。

## 边界

验收器位于 `liveprovider` Build Tag 后，并复用只依赖 Go 标准库的 `provider/openai` Adapter。它不 Import `tiggy-manage-agent`、不增加 Module 依赖、不启动 TMA Server 或数据库，也不把 Provider-specific Type 放进 Core API。

普通 `make verify` 会强制设置 `TIGGY_RUN_LIVE_PROVIDER_TESTS=0`，只编译验收器并运行 SSE/dotenv 与确定性 Provider 故障 Fixture，不建立外部网络连接。真实调用必须同时具备 Build Tag 和 `make provider-live` 设置的显式运行开关。

## Provider 可靠性 Fixture

单独运行 Provider 可靠性层：

```bash
make eval-provider-reliability
```

测试通过注入 `http.RoundTripper`，验证 Adapter 到 Runtime 的完整路径：成功流、HTTP 认证、限流、Quota、Context Length 和不可用故障、非法 SSE、流读取中断、缺少 Finish Reason 的干净 EOF、安全过滤、请求 Deadline 取消和 Runtime Delta 背压。每个场景都要求最终 State 合法、Lifecycle Trace 闭合且只发生一次 HTTP Exchange，用于证明 Fail-fast 配置不会隐式重放失败请求。

这些 Fixture 不使用 Provider Credential、不建立外部 Socket、不依赖 Sleep 时序，也不会产生计费请求；Deadline 场景只等待 Runtime 自己持有的 Context 到期。它们证明的是 Adapter 分类和 Runtime 故障收口，不证明某个外部 Provider 的真实可用率或延迟。

## TMA 配置

命令默认读取相邻仓库：

```text
../tiggy-manage-agent/.env
```

识别以下配置：

```text
TMA_LLM_PROVIDER
TMA_LLM_PROVIDER_TYPE
TMA_LLM_MODEL
TMA_LLM_BASE_URL
TMA_LLM_API_KEY_ENV
TMA_LLM_API_KEY
```

Shell 环境中的同名变量优先于 dotenv。dotenv 始终作为数据解析，不会被 `source` 或执行。API Key 只进入上游 Authorization Header，不会打印、进入 Runtime Message/Event 或写入仓库。

## 执行

下面的命令会产生一次真实、可能计费的文本生成调用：

```bash
make provider-live
```

显式选择其他 dotenv：

```bash
make provider-live TMA_ENV_FILE=/absolute/path/to/.env
```

默认拒绝明文 HTTP。只有受信任的本地 Fixture 才应设置 `TIGGY_LIVE_PROVIDER_ALLOW_HTTP=1`。默认 Deadline 为 90 秒，可通过 `TIGGY_LIVE_PROVIDER_TIMEOUT` 使用正数 Go Duration 覆盖。

## 验收内容

测试要求：

- 真实端点接受 Runtime 投影出的 Provider-neutral Request；
- 流式文本产生 `model.call.delta` Event；
- 最终响应严格等于指定固定标记，没有多余文本；
- Provider Usage 包含正数输入和输出 Token；
- Agent 以 `StatusCompleted` 结束；
- `agenttest.ValidateTrace` 验证 Event Sequence、Parent、操作配对和唯一终态。

成功输出只包含 Provider/Model 标识、Delta 数、Token 数和耗时，不输出 Endpoint、Credential、Prompt、原始 Response 或生成正文。
