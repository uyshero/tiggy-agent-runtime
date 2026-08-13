# Tiggy Agent Runtime

[English](./README.md) | [简体中文](./README.zh-CN.md)

Tiggy Agent Runtime 是一个可嵌入 Go 应用的可靠工具型 Agent Runtime，重点解决运行正确性、结构化异常、上下文与记忆治理、生命周期可观测性和可控扩展。

本仓库是进程内 Runtime Library，不是 OpenAPI 客户端 SDK：

```text
Go 应用 -> import 本模块 -> 在当前进程直接运行 Agent
```

通过 HTTP/SSE 调用 Tiggy Manage Agent 的生成式客户端属于 Enterprise 的独立交付物，不属于本仓库。

## 当前状态

P3 已完成。仓库当前提供与 Provider 无关的协议、可选 OpenAI-compatible Adapter、有边界的顺序 ReAct Loop、Digest 绑定的 Tool 人工审批暂停与恢复、带已完成结果校验恢复能力的可选失败关闭 Durable Transition Hook、Toolkit 和内置执行边界、带外部兼容套件的 MCP Transport、显式 Skill 激活、Token Budget Planning、结构安全 Context Compression、有界大型 Tool Result Offload、Scoped Memory 质量契约、只读 Logging/Metrics/OpenTelemetry Observer、版本化且有完整性链接的 Run Recorder，以及确定性故障评测。当前版本仍未达到生产可用状态。

## Go Module

```text
github.com/uyshero/tiggy-agent-runtime
```

当前包：

```text
message/  模型可见消息、内容块、Tool Call 和 Tool Result
model/    模型生成、流式响应、Usage 和 Provider Error 协议
event/    带版本的 Runtime 生命周期事件信封
tool/     原生 Tool、Result、模型可处理错误和 Runtime Error
toolkit/  并发安全注册表和每次 Run 不可变快照
agent/    有边界的 Agent Loop、State、结构化 RunError 和 Event
orchestrator/  按 Brief 顺序执行任务、验证 Artifact、保存检查点并恢复执行
examples/orchestrator/ 隔离的两任务 Artifact 传递示例
agenttest/ 确定性 Model、Tool、时钟、ID、故障注入和 Trace 校验
safespace/ Root 约束、本地有界读写和确定性目录列表
permission/ 进程内 Tool 外部效果决策和 Action 白名单
approval/ 整批 Tool 审批决策和可序列化 Continuation
builtin/  Runtime 自带的有界、权限感知 Tool
command/  有界命令协议和显式启用的本地进程适配器
mcp/      有界 MCP Tool 发现、调用、生命周期、stdio 和 HTTP
skill/    严格版本化 Skill 发现和显式不可变激活
contextassembly/ 静态 Instruction Stack 和动态上下文边界渲染
contextmanager/ Token Planning、语义压缩分组和可校验摘要替换
artifact/ 版本化大型 Tool Result Offload 和 opaque Object Reference
memory/   Scoped Recall/Observe 协议和有界进程内 Adapter
observe/  结构化日志、有界指标和可选 OpenTelemetry Adapter
record/   隐私感知、完整性链接的 Memory/JSONL Run Journal
provider/openai/ 可选、只依赖标准库的 OpenAI-compatible Model Adapter
evaluation/ 确定性 Runtime 可靠性与 Compressor 质量报告
```

## 运行示例

示例不需要模型凭证：

```bash
go run ./examples/basic-agent
```

实际应用将示例中的 `demoModel` 替换为实现 `model.Model` 的 Provider Adapter，并通过 `agent.Config` 注入。Runtime 不读取全局 Provider 凭证。

## 验证

要求 Go 1.25.12 或更高版本：

```bash
make verify
```

验证包括格式、依赖边界与许可证检查、`go vet`、单元测试、数据竞争检查和确定性 Runtime 可靠性基线。只运行该基线可执行 `make eval-reliability`。

发布元数据单独审计，因为其中包含不应阻塞日常开发、且必须由 Owner 决定的事项：

```bash
make release-audit
make release-verify RELEASE_VERSION=v0.2.2
```

当前技术门禁、阻塞项和 Owner 决策见[发布就绪清单](./docs/release-readiness.zh-CN.md)。审计通过是必要条件，但不代表已获得发布授权。

## 设计文档

- [快速开始与集成手册](./docs/getting-started.zh-CN.md)
- [项目介绍：让 Agent 稳定运行，而不只是跑起来](./docs/introducing-tiggy-agent-runtime.zh-CN.md)
- [架构与边界](./docs/architecture.zh-CN.md)
- [依赖准入规则](./docs/dependencies.zh-CN.md)
- [工程标准](./docs/standards.zh-CN.md)
- [Agent Runtime 语义](./docs/runtime.zh-CN.md)
- [Toolkit 语义](./docs/toolkit.zh-CN.md)
- [SafeSpace 语义](./docs/safespace.zh-CN.md)
- [Permission Policy 语义](./docs/permission.zh-CN.md)
- [Tool 人工审批与恢复语义](./docs/approval.zh-CN.md)
- [Durable Transition Hook](./docs/transition.zh-CN.md)
- [Enterprise Runtime 持久化参考设计](./docs/enterprise-runtime.zh-CN.md)
- [Orchestrator Enterprise 接入](./docs/orchestrator-enterprise.zh-CN.md)
- [Command Runner 语义](./docs/command.zh-CN.md)
- [MCP Client 语义](./docs/mcp.zh-CN.md)
- [Skill 语义](./docs/skill.zh-CN.md)
- [Context Assembly 与 Instruction Stack](./docs/context-assembly.zh-CN.md)
- [Context Manager 语义](./docs/contextmanager.zh-CN.md)
- [Context 诊断快照](./docs/context-diagnostics.zh-CN.md)
- [动态能力激活草案](./docs/dynamic-capability-activation.zh-CN.md)
- [Artifact Offload 语义](./docs/artifact.zh-CN.md)
- [Memory Recall 与 Observe 语义](./docs/memory.zh-CN.md)
- [可观测性语义](./docs/observe.zh-CN.md)
- [Run Recorder 语义](./docs/record.zh-CN.md)
- [Context 与 Memory 故障注入](./docs/fault-injection.zh-CN.md)
- [Agent 测试工具手册](./docs/agenttest.zh-CN.md)
- [真实 Provider 契约验收](./docs/live-provider.zh-CN.md)
- [OpenAI-compatible Provider Adapter](./docs/provider-openai.zh-CN.md)
- [Runtime 可靠性评测](./docs/reliability-evaluation.zh-CN.md)
- [Compressor 质量评测](./docs/compression-evaluation.zh-CN.md)
- [渐进式能力暴露改进方案](./docs/progressive-disclosure.zh-CN.md)
- [内置 Tool 语义](./docs/builtin.zh-CN.md)
- [提取与迁移计划](./docs/migration.md)
- [路线图](./docs/roadmap.md)
- [发布就绪清单](./docs/release-readiness.zh-CN.md)
- [ADR 0001：有限 Model 故障恢复](./docs/adr/0001-model-failure-recovery.md)
- [ADR 0002：Tool 参数 JSON Schema 校验](./docs/adr/0002-json-schema-validation.md)
- [ADR 0003：Doublestar Glob Pattern 匹配](./docs/adr/0003-doublestar-glob-matching.md)
- [ADR 0004：官方 MCP Go SDK](./docs/adr/0004-official-mcp-go-sdk.md)
- [ADR 0005：严格 Skill Manifest 和显式激活](./docs/adr/0005-strict-skill-manifest.md)
- [ADR 0006：显式 Token Budget Planning](./docs/adr/0006-token-budget-planner.md)
- [ADR 0007：结构安全 Context Compression](./docs/adr/0007-context-compression.md)
- [ADR 0008：大型 Tool Result Artifact Offload](./docs/adr/0008-tool-result-artifact-offload.md)
- [ADR 0009：Scoped Memory Recall 与 Observe](./docs/adr/0009-memory-recall-observe.md)
- [ADR 0010：进程内 Memory Adapter](./docs/adr/0010-process-local-memory-adapters.md)
- [ADR 0011：Memory 质量契约](./docs/adr/0011-memory-quality-contract.md)
- [ADR 0012：只读 Observation Adapter](./docs/adr/0012-observation-adapters.md)
- [ADR 0013：版本化、完整性链接的 Run Recorder](./docs/adr/0013-run-recorder.md)
- [ADR 0014：确定性 Context/Memory 故障注入](./docs/adr/0014-deterministic-fault-injection.md)
- [ADR 0015：有界 Provider Context Length 恢复](./docs/adr/0015-provider-context-length-recovery.md)
- [ADR 0016：确定性 Compressor 质量评测](./docs/adr/0016-compressor-quality-evaluation.md)
- [ADR 0017：无正文 Model Context 诊断 Manifest](./docs/adr/0017-model-context-diagnostic-manifest.md)
- [ADR 0018：渐进式能力暴露](./docs/adr/0018-progressive-capability-disclosure.md)
- [ADR 0019：动态能力激活](./docs/adr/0019-dynamic-capability-activation.md)
- [ADR 0020：Durable Transition Hook](./docs/adr/0020-durable-transition-hook.md)
- [ADR 0021：Recovery Continuation API](./docs/adr/0021-recovery-continuation-api.md)
- [兼容性变更记录](./CHANGELOG.md)

## 与 Tiggy Manage Agent 的关系

`tiggy-manage-agent` 是 Enterprise 服务，可以单向依赖本模块。本模块禁止依赖 Enterprise 仓库、数据库 Schema、Worker Runtime、OpenAPI 服务契约和租户模型。

项目采用 Apache-2.0 许可证。公开发布仍是由 Owner 显式控制的独立操作；准备好版本记录不代表版本已经发布。
