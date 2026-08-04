# 发布就绪清单

## 目的

本文把技术验证与发布授权分开。Runtime 仍处于预发布阶段，不能宣称已经达到生产可用。命令通过只能证明本文列出的检查通过，不能替代代码归属、许可证、安全响应能力和公开分发审批。

## 当前技术基线

已经具备以下门禁：

- `make verify` 检查格式、Core/Enterprise 依赖边界、`go vet`、单元测试和 Race；
- 普通 CI 执行 `make verify` 和固定版本的 `make vuln` 漏洞扫描；
- 默认测试使用确定性的本地 Double，不依赖数据库、真实模型和网络；
- `make mcp-compat` 需要下载固定版本的 Node.js 参考 Server，因此保持定时和显式执行；
- Module 不包含相邻仓库的 `replace`，Core 不依赖 Enterprise 或数据库；
- 公开协议变更先记录在 `CHANGELOG.md` 的 `Unreleased` 中。

这些结果只是工程基线，不代表生产就绪。Provider Adapter、持久化 Memory/Artifact Adapter、凭证、隔离、保留策略、SLO 和事故响应属于嵌入应用或 Enterprise 服务。

## 当前阻塞项

当前不得公开发布。编写本基线时仍存在：

- 仓库没有初始 Commit 和可审查历史；
- 项目许可证尚未审批，也不存在 `LICENSE` 文件；
- 仓库归属和公开发布尚未审批；
- `SECURITY.md` 没有支持版本表和受监控的安全联系人；
- Changelog 没有带日期的 Release Section，也没有版本/Tag 基线；
- 在项目 License 和兼容 License Policy 确定前，无法完成 License Compliance Policy 和 CI；
- 尚未确认公开 Module 路径 `github.com/uyshero/tiggy-agent-runtime` 的可用性和所有权。

不得通过虚构姓名、联系人、日期、审批或许可证条款来消除这些阻塞项。

## Owner 必须决策

首次发布前，仓库 Owner 必须决定并记录：

1. 法律意义上的代码 Owner 和项目许可证；
2. GitHub Organization 与 Module Path 是否最终确定且可公开使用；
3. 受监控的私密漏洞报告渠道和支持版本策略；
4. 首个语义化版本和发布日期；
5. 依赖许可证的允许与禁止策略；
6. 谁可以审批、打 Tag、发布、撤回版本并响应安全报告。

## 自动审计

执行不会修改仓库状态的元数据检查：

```bash
make release-audit
```

审计会一次报告所有可检测阻塞项，并与 `make verify` 分离。它检查 Git 历史和工作区清洁度、`LICENSE`、精确 Module Path、无 `replace`、Security Policy 必要结构以及带日期的 Changelog Release Section。

最终候选版本还必须要求指定版本的 Changelog Section 和 `HEAD` 上的精确 Tag：

```bash
make release-verify RELEASE_VERSION=v0.1.0
```

Changelog 标题格式为 `## [0.1.0] - YYYY-MM-DD`。命令不会创建 Commit 或 Tag，也不会检查公开仓库是否可访问。审计要求候选版本已经提交且工作区清洁，因此它在尚未提交的提取工作区中失败是预期行为。

## 首次发布 Checklist

- [ ] 已记录 Owner 和仓库归属；
- [ ] 项目许可证已审批并添加 `LICENSE`；
- [ ] 已审核依赖许可证，并在 CI 执行 Allow/Deny Policy；
- [ ] 已确认公开 Module Path 的所有权和仓库可用性；
- [ ] `SECURITY.md` 包含 `## Supported Versions`、`## Reporting a Vulnerability`，以及 `Security contact: mailto:...` 或 HTTPS Contact 行；
- [ ] 初始历史已审查，候选版本工作区清洁；
- [ ] 已把相关 `Unreleased` 变更移入带日期的语义化版本 Section；
- [ ] Tag Commit 上的 `make release-verify RELEASE_VERSION=vX.Y.Z` 通过；
- [ ] 固定参考 Server 版本的定时 MCP Compatibility 检查为绿色；
- [ ] Release Notes 说明 pre-v1 兼容规则、支持的 Go 版本、已知限制和回滚/撤回 Owner；
- [ ] 获得有权限的 Owner 对公开发布的明确审批。

清单通过后，公开发布仍是独立、由人控制的操作。
