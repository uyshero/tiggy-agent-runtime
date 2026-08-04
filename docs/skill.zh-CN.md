# Skill 语义

## 范围

Skill 是由应用在 Agent Run 之前选定的、可信任且带版本的指令资源。它可以声明 Toolkit Group 和可选文本 Resource。Skill 本身不执行代码、不注册 Tool、不下载依赖、不自动选择自己，也不修改 Agent State。

Core 使用严格的 Tiggy V1 Manifest，不猜测多种不兼容的 `SKILL.md` Frontmatter 方言。未来可以用 Adapter 把外部生态格式转换为本契约。

```text
skills/
  review/
    skill.json
    SKILL.md
    references/
      rules.md
```

```json
{
  "schema": "tiggy.skill/v1",
  "name": "review",
  "version": "1.2.0",
  "description": "Review Go changes.",
  "instructions": "SKILL.md",
  "tool_groups": ["repository.read"],
  "resources": [{"name": "rules", "path": "references/rules.md"}]
}
```

Name 只使用小写 ASCII 字母、数字和中间连字号。Version 是有界不透明标识；Core 会记录它，但不推断排序或兼容性。Path 必须是规范的正斜杠相对路径，不能逃出 Skill 目录。未知字段、重复 JSON Key、重复 Name/Path 和目录名不匹配都会被拒绝。

## 发现与激活

`Discover` 接收由调用方拥有的 `safespace.Reader`。`SafeSpace` 是标准本地实现，可阻止 Symlink 和父路径逃逸。Discovery 只检查 Root 的直接子目录、只读取 `skill.json`，完整校验后返回排序且不可变的 Descriptor。非目录文件会被忽略；每个直接子目录都必须是合法 Skill。Discovery 失败即整体失败，不发布部分 Catalog。

```go
space, err := safespace.OpenSafeSpace("/absolute/app/skills", safespace.Limits{})
if err != nil {
    return err
}
defer space.Close()

catalog, err := skill.Discover(ctx, space, ".", skill.Limits{})
if err != nil {
    return err
}
active, err := catalog.Activate(ctx, skill.ActivationRequest{
    Name: "review", Resources: []string{"rules"},
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    Name: "reviewer", Model: provider, ModelIdentity: identity,
    Toolkit: kit, Skills: active,
})
```

所有 Skill 和 Resource 选择会在内容 I/O 前全部校验，只读取显式选中的 Resource。Instructions、Resource、Tool Group 和 SHA-256 Content Digest 被复制到不可变 Activation Snapshot。后续文件变更不会修改已有 Agent；重新发现和激活由应用显式决定。

新对话中，Agent 把 Activation 追加到 `Config.SystemPrompt`，形成一条 Internal System Message。`Continue` 不重复注入 Skill。Skill Tool Group 与 `Config.ToolGroups` 取并集。存在激活 Skill 时，空并集表示不暴露任何 Tool，不会回退到整个 Toolkit。未知 Group 在 `agent.New` 时失败。

`run.started` 只记录 Skill Name、Version、Digest 和最终 Tool Group，默认 Event 不包含 Instructions 和 Resource 正文。

## 默认上限

```text
Catalog Skills              128
Manifest                     64 KiB / Skill
Instructions                128 KiB / Skill
Declared Resources           32 / Skill
Selected Resource           256 KiB / Resource
Rendered Activation         512 KiB Total
```

内容必须是非空 UTF-8 文本，且不含 Null Byte。Catalog 和 Activation 在发布前复制值。每次 Source Call 前后检查 Context 取消。Source panic 转为 `source_failed`，不会崩溃 Host Process。Reader 由调用方关闭；Catalog 并发激活时，Reader 必须支持并发读。

## 信任边界

激活内容具有 System Instruction 权限，应用只能从已授权来源激活经审核 Skill。SafeSpace 只限制本地路径，不提供 Publisher Trust、签名校验、Tenant 授权或远程包完整性。

Core 不提供全局 Skill Search Path、环境变量驱动加载、Model 自动选 Skill、Remote URL Fetch、Package Installer、Script Hook 或动态 Tool 注册。这使权限在 Agent 构造时保持可见，企业分发、审批和租户管理仍留在 Core 之外。
