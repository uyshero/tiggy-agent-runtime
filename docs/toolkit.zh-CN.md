# Toolkit 语义

## 注册与快照

Toolkit 是并发安全注册表。注册时 Runtime 只读取一次 Tool Definition、编译 Input Schema，并保存自己的 Definition 副本。每次 Agent Run 获取不可变 View；之后注册的新工具只影响后续 Run。

```go
kit.RegisterWith(toolkit.Registration{
    Tool: nativeTool,
    Namespace: "crm",
    Groups: []string{"read", "customer"},
})
```

示例 Tool 原名为 `lookup` 时，模型可见名称为 `crm.lookup`。Name 和 Namespace 使用由字母、数字、`_`、`-`、`.` 组成的有界 ASCII 名称；`.` 分隔的 Segment 不能为空。

Group 不进入 Tool Call 协议，只控制能力投影。`SnapshotGroups` 返回多个 Group 的并集并拒绝未知 Group；不传 Group 时返回空 View。`agent.Config.ToolGroups` 让一个 Agent 只向模型暴露选中的工具；省略该字段时保持兼容默认值，暴露全部已注册工具。

`View.DefinitionBytes()` 返回该不可变 View 中 Tool 契约的无正文聚合字节数。它用于诊断和预算测试，不用于改写或摘要 Schema 正文。

MCP Client 通过 `mcp.Client.Tools` 返回普通 `tool.Tool` Adapter。注册时应显式使用 Namespace，避免不同 Server 的 Tool 冲突。Toolkit 只拥有不可变注册 Snapshot；MCP Client 生命周期属于应用，Catalog 变化的 Reconnect 后必须重建注册。

## JSON Schema

- Input Schema 在注册时按 Draft 2020-12 编译；
- 开启 Format Assertion，关闭 Content Assertion；
- 支持同一 Schema 内的 `$defs` 和本地 Fragment；
- 禁止文件和远程 `$ref`，Schema 编译不会访问外部资源；
- 参数不符合 Schema 时返回模型可处理的 `invalid_tool_arguments`；
- Schema 编译或校验 panic 被隔离，不会导致宿主进程退出；
- Output Schema 暂不执行，等待结构化 Tool Output Contract 明确。

详细依赖决策见 [ADR 0002](./adr/0002-json-schema-validation.md)。

## 并发类别与批处理

Tool Definition 的 `ConcurrencyClass` 有两种值：

- 空值或 `sequential`：保守默认，是批次中的独占顺序屏障；
- `safe`：声明 Tool 实现和外部资源允许多个调用重叠。

`View.CallBatch` 将连续的 `safe` 调用放入有界 worker 执行；顺序 Tool 会等待前一并发阶段结束，并阻止后一阶段提前开始。默认并发数是 4，硬上限是 64，结果始终按输入 Call 顺序返回。

Batch 启动 worker 前深拷贝 Tool Call 和 Arguments。调用返回前会等待所有 worker 退出；取消通过 Context 传播。Tool 忽略 Context 时 Runtime 无法强制终止该调用。

当前 Agent Loop 仍按模型给出的顺序逐个执行 Tool。启用 Agent 级并发前，还需要明确批次部分失败和外部副作用策略；Toolkit 不会通过新增 API 静默改变现有 Run 语义。

## 错误与所有权

- Schema 和模型可修正业务错误返回 `tool.Error`；
- 普通 error、非法 Tool Result、Tool panic 和 Schema Runtime Failure 返回 `tool.RuntimeError`；
- Tool 主动返回的 `tool.RuntimeError` 会保留，不会折叠成通用执行失败；
- Toolkit 复制参数和结果 Buffer，但不会复制或关闭调用方注入的 Tool 实例；
- 声明 `safe` 的 Tool 必须在多个 Agent Run 和 Batch worker 间保持并发安全。
