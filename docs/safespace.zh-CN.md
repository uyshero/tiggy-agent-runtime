# SafeSpace 语义

## 边界

`safespace.OpenSafeSpace` 使用 Go `os.Root` 打开一个由调用方指定的目录。所有路径必须相对该 Root；绝对路径、`..` 逃逸以及指向 Root 外部的 Symlink 会失败。

`safespace.NormalizePath` 向内置 Tool 和注入的 FileSystem Adapter 暴露同一套相对路径规范化规则，使 Permission Decision 与实际 I/O 使用同一个规范化 Target。

```go
safeSpace, err := safespace.OpenSafeSpace("./agent-work", safespace.DefaultLimits())
if err != nil {
    return err
}
defer safeSpace.Close()
```

SafeSpace 是进程内文件执行环境，不是租户 Workspace、对象存储或数据库记录。调用方拥有它的生命周期；Agent 和 Toolkit 不会自动关闭注入的 FileSystem。

## 操作

- `ReadFile` 只读取普通文件，并同时检查 Stat Size 和流式读取上限；
- `WriteFile` 检查写入上限，按需创建父目录，并在同一目录写临时文件后 Rename；
- `List` 要求目录，只列一层，限制 Entry 数量并按名称排序；
- 所有操作在开始和返回前检查 Context；标准文件系统调用本身不可抢占。

默认限制是单文件读写 4 MiB、单次 List 10,000 项。生产应用应按 Agent 场景收紧。

## 原子性与 Durability

Write 使用同目录临时文件和 Rename，避免调用方看到部分写入内容。取消发生在 Rename 前时，临时文件会被清理，目标文件不变。

这不是 Durable Write：当前实现不对文件和父目录执行完整 `fsync` 协议，不保证进程崩溃、系统崩溃或断电后数据一定落盘。需要持久化保证的企业流程应使用明确的 Artifact/Storage Adapter，而不是把 SafeSpace 当数据库。

## 安全限制

`os.Root` 防止普通路径和 Symlink 逃逸，但不禁止跨文件系统 Mount、Linux Bind Mount、`/proc` 映射或 Root 内已有的设备文件。ReadFile 的普通文件检查降低了设备读取风险，但调用方仍应提供专用、最小权限、无危险 Mount 的目录。

Root 内存在对抗性进程持续修改目录结构时，安全能力还受操作系统实现限制。Sandbox、容器和命令执行属于后续独立边界，SafeSpace 本身不是 Sandbox。
