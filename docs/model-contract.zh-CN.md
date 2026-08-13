# Model 契约说明

`model.Model` 是唯一必需的 Provider 执行契约。可选 Token 计数使用独立的
`model.TokenCounter` 接口。Provider Credential、Client 生命周期、Model Catalog
和部署选择仍由 Adapter 或应用负责。

## Tool Output Schema

`model.ToolDefinition.OutputSchema` 可以来自 MCP 或其他 Provider Catalog。
Runtime 在克隆 Definition、计算 Toolkit Definition Digest、记录不含正文的
Definition Byte Count，以及通过兼容 Adapter 传递 Definition 时保留该字段。

Runtime 不会使用 `OutputSchema` 校验 `tool.Result`。Tool Result 是多个带类型的
Message Content Block 加可选 opaque JSON State，不是一个具有明确 Schema 目标的
JSON Instance。拥有结构化 Tool 契约的应用，可以在 Tool 或 Adapter 内构造 Result
之前校验结构化值。

## Capabilities

遗留的 `model.Capabilities` 只是描述性值，现已标记 Deprecated。Agent 不会用它
选择 Model、暴露 Tool、改变 Request 形态、选择 Fallback 或绕过校验。在正式接受
带版本的能力发现与选择协议之前，Adapter 应把 Provider Catalog Metadata 保持在
私有实现中，应用通过显式配置做选择。

这样可以避免 Provider-specific Flag 变成隐式 Runtime Policy，并确保 Enterprise
Tenant、Entitlement、Routing 和 Deployment Record 不进入 Core。
