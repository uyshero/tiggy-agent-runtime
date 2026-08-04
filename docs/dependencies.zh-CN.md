# 依赖准入规则

## 当前基线

`v0.0.x` 的基础协议包只使用 Go 标准库。Toolkit 使用经 ADR 审批的 JSON Schema 依赖，内置搜索使用经 ADR 审批的 Doublestar Matcher，MCP Transport 兼容性使用经 ADR 审批的官方 Go SDK，可选 Observation Adapter 使用经 ADR 审批的官方 OpenTelemetry Module；不为路线图中的未来能力预先添加依赖。

依赖只能服务于已经实现的能力。路线图中的未来功能不能作为提前添加依赖的理由。

## 分层规则

### 基础协议包

以下包原则上保持标准库依赖：

```text
message
model
event
permission
command
Agent State 和 Runtime Contract
```

基础包禁止在公开签名中暴露第三方类型。

### Runtime 包

Agent、Toolkit、内置 Tool、Context、Artifact、Memory 和 Observe 可以在确实降低协议、并发或安全风险时引入经过审核的小型依赖，但公开 API 仍使用 Tiggy 自有接口和值类型。

### Adapter 包

Model Provider、MCP、OpenTelemetry、Vector Store、Remote FileSystem 和隔离型 Command Runner Adapter 可以依赖官方或成熟 Client，但 Adapter 依赖不能渗透到基础协议包。`mcp` 的私有 Transport Adapter 引入官方 SDK，但公开 API 仍只暴露 Tiggy 和 Go 标准库类型。

`command/local` 是仅使用标准库的本地进程 Adapter，只依赖 `command`、`safespace` 和 `os/exec`，不增加 Go Module 依赖。应用必须显式 Import 它；`builtin.bash` 不会传递引入本地执行能力。

`skill` 使用标准库严格 JSON 解析和仅依赖标准库的 `safespace.Reader` 契约。Skill 发现与激活不增加 Go Module、YAML Parser、Package Manager 或 Node.js 依赖。详见 [ADR 0005](./adr/0005-strict-skill-manifest.md)。

`contextmanager` 只使用已有 `model.TokenCounter` Port 和标准库算术。Token Planning 不增加 Tokenizer 依赖，Provider-specific Tokenization 仍留在注入 Adapter 中。语义分组和摘要替换也只使用 Tiggy Protocol Value 与标准库；Compressor 实现仍由调用方注入。详见 [ADR 0006](./adr/0006-token-budget-planner.md) 和 [ADR 0007](./adr/0007-context-compression.md)。

`artifact` 只使用 SHA-256、JSON、Tiggy Message/Tool Value 和标准库。Core 不包含 Object Store SDK；应用注入 `artifact.Store` Adapter，并负责自己的 Durability、Authorization、Encryption、Retention 和 Cleanup 语义。详见 [ADR 0008](./adr/0008-tool-result-artifact-offload.md)。

`memory` 只使用 Tiggy Message Value、`sync` 和标准库。Noop 和 InMemory Reference Adapter 不增加 Module 或持久化依赖。Core 不包含数据库、Vector Client、Tokenizer 或 Embedding 依赖；应用注入 Enterprise `memory.Port` Adapter，后端 Identity/Record Type 保持私有。详见 [ADR 0009](./adr/0009-memory-recall-observe.md) 和 [ADR 0010](./adr/0010-process-local-memory-adapters.md)。

`observe` 只使用 Event/Model Value 和 Go 标准库。`observe/otel` 固定官方 OpenTelemetry Go API/SDK v1.44.0；SDK Package 用于验证 Export Behavior。OTel Type 只存在于可选 Adapter；Provider、Exporter、后台资源和 Shutdown 均由应用拥有。详见 [ADR 0012](./adr/0012-observation-adapters.md)。

`record` 只使用 Event/Model Value、JSON、SHA-256、有界标准库 I/O 和同步原语。Memory/JSONL Journal 不增加数据库、Object Store、Logging 或 Replay 依赖。详见 [ADR 0013](./adr/0013-run-recorder.md)。

`agenttest` Fault Injection 只使用已有 Tiggy Context/Memory/Model Contract、Context Cancellation 和标准库同步原语，不增加 Chaos、Timing、Random、Proxy 或基础设施依赖。详见 [ADR 0014](./adr/0014-deterministic-fault-injection.md)。

## 禁止依赖

- `tiggy-manage-agent` 内部包；
- Core 中的 PostgreSQL Client 或 `database/sql`；
- TMA HTTP Handler、Worker、Tenant 和数据库 Record；
- TMA 生成式 OpenAPI Client；
- Browser、UI 和 Node.js 构建依赖；
- 未经 ADR 审批且未隔离在可选 Adapter 中的 CGO 依赖。

## 新依赖准入清单

每个直接依赖的 PR 必须说明：

1. 服务于哪个已实现能力；
2. 为什么标准库或小型本地实现不够；
3. License 与代码归属；
4. 维护活跃度和发布历史；
5. 已知安全公告；
6. 传递依赖数量和二进制体积影响；
7. 是否启动 goroutine 或拥有网络/文件资源；
8. 取消、超时和关闭行为；
9. 是否进入公开 API；
10. 替换或移除方案。

## 待 ADR 决策的依赖类别

| 能力 | 必须评估 |
|---|---|
| JSON Schema | 已由 [ADR 0002](./adr/0002-json-schema-validation.md) 决定 |
| Glob 匹配 | 已由 [ADR 0003](./adr/0003-doublestar-glob-matching.md) 决定 |
| MCP Transport | 已由 [ADR 0004](./adr/0004-official-mcp-go-sdk.md) 决定；固定版本的外部 stdio 矩阵详见 [MCP Client 语义](./mcp.zh-CN.md#外部兼容性) |
| OpenTelemetry | 已由 [ADR 0012](./adr/0012-observation-adapters.md) 决定：可选 Adapter、应用拥有 Provider、有界语义约定 |
| Tokenizer | Provider 精度、Fallback 和模型覆盖 |
| Vector Memory | Port 已由 [ADR 0009](./adr/0009-memory-recall-observe.md) 决定；Backend Client、Embedding、Index 和质量 Policy 仍由 Adapter 决定 |
| 隔离命令执行 | 先稳定 `command.Runner` Contract，再选择 Docker/K8s/Remote Client |

## 版本和可重复构建

- `go.mod` 和 `go.sum` 同时提交；
- Release Commit 使用明确版本，不依赖分支；
- 依赖变更后运行 `go mod tidy`；
- CI 使用 `go.mod` 声明的 Go 版本；
- Public Release 不依赖任何相邻仓库；
- 本地联调使用开发者 `go.work`，Release `go.mod` 不提交相对路径 `replace`。

首次公开发布前，CI 必须增加漏洞和 License 检查。漏洞豁免必须记录评估、Owner、到期时间和补偿措施。
