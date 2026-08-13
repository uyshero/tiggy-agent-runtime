# 发布就绪清单

## 目的

本文把技术验证与发布授权分开。Runtime 仍处于预发布阶段，不能宣称已经达到生产可用。命令通过只能证明本文列出的检查通过，不能替代仓库归属、实际安全响应能力和公开分发审批。

## 当前技术基线

已经具备以下门禁：

- `make verify` 检查格式、Core/Enterprise 依赖边界、已审核的依赖许可证清单、`go vet`、单元测试和 Race；
- 确定性 Fuzz Seed 会在普通 Package Test 中运行；`make fuzz` 和 `make benchmark` 是可选的解析器健壮性及分配/吞吐基线诊断；
- 普通 CI 执行 `make verify` 和固定版本的 `make vuln` 漏洞扫描；
- 默认测试使用确定性的本地 Double，不依赖数据库、真实模型和网络；
- `make mcp-compat` 需要下载固定版本的 Node.js 参考 Server，因此保持定时和显式执行；
- Module 不包含相邻仓库的 `replace`，Core 不依赖 Enterprise 或数据库；
- 公开协议变更记录在带日期的版本 Section 中；已有发布基线后的新变更先进入 `Unreleased`。

这些结果只是工程基线，不代表生产就绪。Provider Adapter、持久化 Memory/Artifact Adapter、凭证、隔离、保留策略、SLO 和事故响应属于嵌入应用或 Enterprise 服务。

## 当前阻塞项

`v0.2.3` Release 记录了强化后的 Sequential Brief Orchestrator Event 与 JSON
契约。这记录了发布基线，但不能证明法律归属或安全响应已经具备实际运维能力。

- 公开 Module 路径 `github.com/uyshero/tiggy-agent-runtime` 的可用性和所有权仍需 Owner 确认；
- `security@tiggy.cloud` 的投递与监控需要完成实际运维确认；
- 仓库尚未记录完整的组织级发布与安全响应角色分工。

Apache-2.0、私密安全报告地址以及截至 `v0.2.3` 的发布日期已经记录。Workspace
Owner 已于 2026-08-13 明确授权发布 `v0.2.3`。这不代表上面的法律归属或安全
运维事项已经确认。

## Owner 必须决策

已经记录的决定：

- 项目许可证：Apache-2.0；
- 首个 Tag 基线：`v0.1.0`，日期为 2026-08-04；
- 最新公开 Release 基线：`v0.2.3`，日期为 2026-08-13；
- 当前没有记录更晚的 Release 候选，新改动保留在 `Unreleased`；
- Workspace Owner 已于 2026-08-13 授权发布 `v0.2.3`；
- 发布后的安全策略：支持最新 `0.2.x`，私密报告发送到 `security@tiggy.cloud`。

未来再次公开发布前，仓库 Owner 仍需记录：

1. 法律意义上的代码 Owner，以及 GitHub Organization/Module Path 是否最终确定且可公开使用；
2. 谁可以审批、打 Tag、发布、撤回版本并响应安全报告；
3. 对本次公开发布操作的明确授权。

## 自动审计

执行不会修改仓库状态的元数据检查：

```bash
make release-audit
```

审计会一次报告所有可检测阻塞项，并与 `make verify` 分离。它检查 Git 历史和
工作区清洁度、`LICENSE`、精确 Module Path、无 `replace`、Security Policy
必要结构以及带日期的 Changelog Release Section。`make verify` 会另外运行
`make api-compat`，与可达的最新稳定 Tag 比较公开 Go API。

最终候选版本还必须要求指定版本的 Changelog Section 和 `HEAD` 上的精确 Tag：

```bash
make release-verify RELEASE_VERSION=v0.2.3
```

Changelog 标题格式为 `## [0.2.3] - YYYY-MM-DD`。命令不会创建 Commit 或 Tag，也不会检查公开仓库是否可访问。审计要求候选版本已经提交且工作区清洁，因此它在尚未提交的提取工作区中失败是预期行为。

## 当前发布 Checklist

- [ ] 已记录 Owner 和仓库归属；
- [x] Apache-2.0 已审批并添加 `LICENSE`；
- [x] 已审核依赖许可证，并在 CI 执行 Allow/Deny Policy；
- [ ] 已确认公开 Module Path 的所有权和仓库可用性；
- [x] `SECURITY.md` 已包含所需的支持版本、报告方式和 Contact Contract；
- [x] 本地 `v0.1.0` Tag 指向对应的发布提交；
- [x] `v0.2.3` Release 内容已归入带日期的 Changelog Section；
- [x] Orchestrator 改动已归入带日期的 `v0.2.2` Release Section；
- [x] `v0.1.0` 基线已记录在 `## [0.1.0] - 2026-08-04`；
- [x] `v0.1.1` 已记录在 `## [0.1.1] - 2026-08-05`；
- [x] `v0.1.2` 已记录在 `## [0.1.2] - 2026-08-11`；
- [x] `v0.2.0` 已记录在 `## [0.2.0] - 2026-08-11`；
- [x] `v0.2.1` 已记录在 `## [0.2.1] - 2026-08-12`；
- [x] `v0.2.2` 已记录在 `## [0.2.2] - 2026-08-13`；
- [x] `v0.2.3` 已记录在 `## [0.2.3] - 2026-08-13`；
- [x] Tag Commit 上的 `make release-verify RELEASE_VERSION=v0.2.3` 通过；
- [ ] 固定参考 Server 版本的定时 MCP Compatibility 检查为绿色；
- [x] Release Notes 说明 pre-v1 兼容规则、支持的 Go 版本、已知限制和回滚/撤回 Owner；
- [x] Workspace Owner 已于 2026-08-13 明确批准发布 `v0.2.3`。

任何后续版本的公开发布仍是独立、由人控制的操作。
