# 内置 Tool 语义

## 构造与所有权

内置 Tool 是普通 `tool.Tool` 实现。应用显式注入依赖，再注册到 Toolkit：

```go
space, err := safespace.OpenSafeSpace("./agent-work", safespace.DefaultLimits())
if err != nil {
    return err
}
defer space.Close()

policy, err := permission.NewActionPolicy(
    permission.ActionRead,
    permission.ActionWrite,
    permission.ActionEdit,
    permission.ActionSearch,
    permission.ActionExecute,
)
if err != nil {
    return err
}
read, err := builtin.NewRead(builtin.ReadOptions{FileSystem: space, Permission: policy})
if err != nil {
    return err
}
write, err := builtin.NewWrite(builtin.WriteOptions{FileSystem: space, Permission: policy})
if err != nil {
    return err
}
edit, err := builtin.NewEdit(builtin.EditOptions{FileSystem: space, Permission: policy})
if err != nil {
    return err
}
glob, err := builtin.NewGlob(builtin.GlobOptions{FileSystem: space, Permission: policy})
if err != nil {
    return err
}
grep, err := builtin.NewGrep(builtin.GrepOptions{FileSystem: space, Permission: policy})
if err != nil {
    return err
}
runner, err := local.New(local.Options{
    RootPath:  "./agent-work",
    ShellPath: "/bin/bash",
})
if err != nil {
    return err
}
bash, err := builtin.NewBash(builtin.BashOptions{Runner: runner, Permission: policy})
if err != nil {
    return err
}
kit, err := toolkit.New(read, write, edit, glob, grep, bash)
```

Tool Name 固定为 `builtin.read`、`builtin.write`、`builtin.edit`、`builtin.glob`、`builtin.grep` 和 `builtin.bash`。当前它们都声明为顺序执行。FileSystem、Policy 和 Runner 的生命周期属于调用方，Tool 不会关闭注入依赖。

nil Policy 默认拒绝全部调用；应用必须显式使用 `permission.AllowAll` 才能放行所有操作。Bash 也没有默认 Runner：启用本地进程执行需要像上例一样显式 Import 并构造 `command/local`。Shell Path 是平台配置，不是 Core 的可移植默认值。

## 路径与权限顺序

Tool 先校验 JSON 参数，再通过 `safespace.NormalizePath` 规范化路径，然后才调用 Permission Policy。因此 Policy 和 FileSystem 接收到的是同一个已清理相对路径。绝对路径、父目录逃逸、把 Root 当作文件、NUL 字节和超过 4096 Bytes 的路径都会在外部效果发生前被拒绝。

操作顺序是：

```text
JSON Schema 与参数校验
  -> 规范化相对路径
  -> 在可行位置提前校验内容/输出上限
  -> Permission Policy
  -> FileSystem 操作
  -> 有界 Tool Result
```

Read 请求 `permission.ActionRead`，Write 请求 `permission.ActionWrite`，Edit 请求 `permission.ActionEdit`，Glob/Grep 请求 `permission.ActionSearch`。

Bash 仅以规范化工作目录作为 Target 请求 `permission.ActionExecute`。完整脚本在授权后才传给 Runner，永远不会进入 `permission.Request`。

递归搜索只对规范化 Search Root 授权一次。需要更细对象级授权的 FileSystem Adapter 必须在 List 和 ReadFile 中强制执行；Core 不会为每个发现的 Entry 发起一次外部 Policy 调用。

## Read

Read 接收 `{"path":"..."}` 并返回 UTF-8 文本。模型可见文件内容默认最多 128 KiB，可以通过 `ReadOptions.MaxBytes` 收紧或显式增大。FileSystem 仍必须实施自身资源上限，因为 Read 只能在收到 Buffer 后检查大小。

非空文件返回一个摘要 Content Block 和一个文件内容 Block。空文件返回非空摘要，保证 Tool Result 符合协议。二进制或其他非法 UTF-8 内容返回 `unsupported_file_encoding`；Read 不会静默转码，也不会把二进制直接塞入模型上下文。

摘要里会带上 `file_digest=sha256:...` 标记，`Result.State` 也会带 `path` / `size_bytes` / `digest` 的机器可读快照，调用方可以把它作为 `builtin.edit` 的 `expected_digest`。

## Write

Write 接收 `{"path":"...","content":"..."}`，以 `0644` Mode 创建或替换文件。内容默认最多 1 MiB，可通过 `WriteOptions.MaxBytes` 调整。内容大小在 Permission 和 I/O 前检查。

SafeSpace 当前通过同目录临时文件和 Rename 实现替换；其他 FileSystem 可以有不同的原子性和持久性保证。Write 本身不承诺 Durable Persistence、Append、冲突检测或外部效果 Exactly-once。

## Edit

Edit 接收 `path`、非空 `old_text`、`new_text`，以及可选的 `replace_all`、`expected_digest` 和 `dry_run`。默认要求 `old_text` 恰好出现一次：没有匹配返回 `text_not_found`，多处匹配返回 `ambiguous_match`。设置 `replace_all=true` 后替换全部不重叠精确匹配。

Edit 默认最多读写 1 MiB，要求 UTF-8，并以 `0644` Mode 写回。替换前后内容相同时不会执行写入。如果提供了 `expected_digest` 且当前文件 digest 不一致，Tool 会返回 `file_changed` 而不是写入。如果 `dry_run=true`，Tool 只返回计划中的变更和 digest，不会写盘。

Edit 是 FileSystem 上的 Read-Modify-Write，不具备 Compare-and-Swap 契约；只有调用方带上 `expected_digest` 时，才能发现 ReadFile 与 WriteFile 之间的并发写入。SafeSpace 的替换原子性只能避免看到部分内容，不能防止 Lost Update，也不承诺 Durability。

## Glob 与 Grep

Glob 和 Grep 从可选 `path`（默认 Root）开始递归遍历，并共享以下 `SearchLimits`：

```text
Entry         10,000
Result         1,000
Depth             64
Result Text   128 KiB
File Read       1 MiB
```

字段为零时使用默认值。Entry 或 Depth 耗尽返回 `search_limit_exceeded`；Result 数量或输出字节耗尽时返回明确标记为 Truncated 的部分结果。遍历按每层 Entry Name 排序，并拒绝 FileSystem 返回的非法 Entry Name。

Glob 使用相对于请求 Path 的成熟 Doublestar Pattern，支持 `**`、字符类和 Alternatives。结果使用相对于 Root 的路径，目录以 `/` 结尾；Pattern 能匹配时也会包含 Dotfile。

Grep 使用 Go 基于 RE2 的 `regexp` 引擎编译 `pattern`，并逐行匹配。可选 `include` 是 Doublestar 文件过滤器，默认为 `**`。输出包含带引号路径、从 1 开始的行号和文本；单行最多返回 4096 Bytes。搜索期间消失、不可访问、超过文件上限或不是 UTF-8 的文件会被跳过并计数，未知 FileSystem 故障仍是致命 RuntimeError。

## Bash

Bash 接收必填 `script` 和可选 `path`，`path` 默认为 Root。它校验并规范化输入、检查 `execute` 权限，再调用注入的 `command.Runner`。Tool 默认限制为 30 秒、32 KiB 脚本和 128 KiB stdout/stderr 合计结果。Runner 也必须执行自身资源上限，因为 Tool 无法约束任意 Runner 内部消耗。

Shell 非零退出是普通 Tool Result，其中包含 Exit Code 和已捕获 stdout/stderr，不是 Runtime Failure。输出必须是 UTF-8。Runner 目录/输出错误是模型可处理错误；Runner 启动、契约、panic 或基础设施故障会作为 RuntimeError 终止 Agent Run。

可选 `command/local` Runner 使用 `os/exec`，默认也限制 32 KiB 脚本和 128 KiB 合计输出。它默认不继承 Host Environment，只提供固定 `PATH`、以 Root 作为 `HOME` 和 UTF-8 Locale；显式提供 `Environment` 会完全替换这组默认值。它的 Root 仅约束初始工作目录，脚本仍可访问 Host 绝对路径、网络、操作系统权限能访问的凭证和 Root 之外资源。需要隔离时必须使用 Container、VM、Remote Executor 或其他 Runner。

取消会通过 `os/exec` 终止直接启动的 Shell，但不能撤销已产生的文件、网络或其他外部效果。本地 Adapter 的 `WaitDelay` 只限制等待被继承输出 Pipe 的时间，不提供进程树隔离；子孙进程可能因操作系统而比 Shell 存活更久。Bash 和 Runner 都不承诺 Exactly-once 或回滚。

## 失败

模型可处理且不可重试的 Tool Error 包括：

- `permission_denied`；
- `invalid_tool_arguments`、`invalid_path`、`invalid_directory` 和 `invalid_pattern`；
- `file_not_found`、`file_access_denied`、`not_regular_file` 和 `not_directory`；
- `file_too_large` 和 `unsupported_file_encoding`；
- `text_not_found`、`ambiguous_match` 和 `search_limit_exceeded`；
- `command_too_large`、`command_timeout`、`command_output_limit` 和 `unsupported_command_output`。

Permission 扩展故障保留稳定 RuntimeError Code；未分类 FileSystem 故障使用 `file_system_failed`。非法 Runner 结果、启动故障、panic 和未分类 Runner 故障仍是 RuntimeError。Toolkit 会保留 Tool 主动返回的 `tool.RuntimeError`，这些故障会终止 Agent Run，不能伪装成普通权限拒绝。

Context 取消和外部 Deadline Error 直接传播。Toolkit 和内置 Tool 都无法抢占忽略 Context 的 Policy、FileSystem 或 Runner 实现。
