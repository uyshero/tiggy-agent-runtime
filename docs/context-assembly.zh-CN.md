# Context Assembly 与 Instruction Stack

## 目标

Context Assembly 用来让模型请求的组成方式变得明确，但不把 Core 变成策略引擎。它把稳定权威和运行期证据分开：

```text
Static Instructions
  - 应用配置的 SystemPrompt
  - 显式激活的 Skill 指令与选中 Resource

=========== DYNAMIC CONTEXT BELOW ===========

Dynamic Context
  - Recall Memory 证据
  - Context Compression 摘要
  - 未来的有界 Catalog 或任务状态投影
```

这条边界会进入模型上下文。它的目的就是告诉模型：边界下面的内容是上下文证据，不是覆盖静态指令的权威。

## Runtime 行为

当配置了 `SystemPrompt` 或激活了 Skill 时，Agent 会通过 `contextassembly.Render` 创建一条 Internal System Message。该消息带有 `metadata.kind=instruction_stack`，记录静态 Segment 类型，并记录内置 Segment 类型的聚合文本字节数。完整渲染文本只在新对话开始时写入 `Agent.State`；`Continue` 会复用已有 State，不重复注入。

应用可以通过 `agent.Config.Instructions` 增加额外静态 Segment：

```go
runtime, err := agent.New(agent.Config{
    Name:         "assistant",
    SystemPrompt: "遵守 Runtime 安全规则。",
    Instructions: []contextassembly.Segment{
        contextassembly.StaticSegment(
            contextassembly.KindProject,
            "repo/AGENTS.md",
            "遵循仓库既有代码风格和测试方式。",
        ),
        contextassembly.StaticSegment(
            contextassembly.KindTaskRule,
            "task",
            "只围绕本次需求做聚焦改动。",
        ),
        contextassembly.StaticSegment(
            contextassembly.KindPolicy,
            "security",
            "不要暴露 Secret 或 Credential。",
        ),
    },
    Model: provider,
    ModelIdentity: identity,
})
```

静态区渲染顺序是 SystemPrompt、配置的 Instructions、已激活 Skills。`agent.New` 会复制并校验这些 Segment。

固定诊断类型包括：

- `system_prompt`
- `project`
- `task_rule`
- `policy`
- `knowledge`
- `skill`

Model Context Manifest 会把这些类型的聚合字节数作为 metadata-only 的预算信号暴露出来。它不会暴露 Segment 正文、Source 名称、Skill 名称或自定义 Kind 名称。自定义 Segment Kind 仍可以渲染进 Instruction Stack，但 Core 不会为它们生成新的协议字段。

Memory Recall 仍然是 request-only。它作为独立 Internal System Message 插入在前导 System Message 之后，因此在 Provider Request View 中位于 Instruction Stack 边界之下，并且不会进入 `Agent.State`。

Compression Summary 也是 Context Manager 生成的 Internal System Message。它属于动态 Request View Artifact，不会改写完整 State。

## 非目标

这一层不决定哪个 Policy 优先、不授权 Tool、不审批外部副作用、不选择模型、不持久化 Memory，也不改写 Tool Schema。它只给 Runtime 和测试一个确定的位置，用来组装和解释模型可见上下文。

生产应用仍应把租户策略、审批身份、持久化 Memory、删除、审计存储等能力留在 Core 外部。
