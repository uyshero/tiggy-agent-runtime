# Command Runner 语义

## 契约

`command.Runner` 是有界 Shell 执行的 Core Port。Request 包含非空脚本和相对 Root 的规范化工作目录；Result 拥有自己的 stdout/stderr Buffer 并携带 Exit Code，`-1` 表示终止时没有可移植数字退出码。非零 Exit Code 是合法 Result，不是 Runner Error。

在多个 Tool Call 间共享的实现必须并发安全、响应 Context 取消，并限制脚本大小和捕获输出。调用方提供有限 Deadline；更强的 CPU、内存、进程数和隔离控制由所选后端决定。基础设施故障使用强类型 `command.Error` Code。Runner 契约不负责授权、选择 Shell、定义安全边界、持久化执行，也不承诺回滚或 Exactly-once。

`builtin.bash` 必须注入 Runner。这使 Host 进程权限不会默认进入 Tool 构造路径，并允许应用注入 Container、VM、Remote Executor、Test Double 或特定 Policy Adapter。

## 本地 Adapter

`command/local` 是基于标准库 `os/exec` 的显式启用 Adapter。构造时必须提供已存在的 Root 目录和绝对、可执行 Shell Path；两者都会解析符号链接。每次 Request 的目录必须存在，且在进程启动前解析到 Root 内。

该约束只适用于初始工作目录，不限制绝对路径、Shell 重定向、网络访问、操作系统凭证、进程创建、Mount、Device 或其他 Host 资源。除非外层进程已被隔离，否则不应使用该 Adapter 执行不可信脚本。

默认值为：

```text
脚本              32 KiB
stdout + stderr   128 KiB
WaitDelay              2s
环境变量          固定 PATH、HOME=Root、UTF-8 Locale
```

非 nil 自定义 Environment 会被复制和校验，然后原样使用；Host Environment 永远不会被隐式追加。stdout 和 stderr 分开捕获，因此不表示两条 Stream 之间的交错顺序。输出超限会取消直接 Shell，并以 `command_output_limit` 返回有界的部分 Buffer。

调用方通过 Context 提供超时或取消。取消不能撤销已产生效果。`exec.CommandContext` 只针对直接启动的 Shell，`WaitDelay` 仅限制等待被继承 I/O Pipe 的时间；二者都不保证终止进程树。需要更强生命周期或安全保证的应用必须注入其他 Runner。

## 所有权与失败规则

- Request String 保持调用方拥有的不可变值；
- 本地 Adapter 返回的 Result Buffer 是由调用方拥有的新副本；
- Runner 拥有自己创建的每个进程，直到 `Run` 返回；
- Core 生成的非法 Request 和不可能的 Runner Result 属于集成故障；
- 无效/不可用工作目录和输出耗尽在 `builtin.bash` 中属于模型可处理错误；
- 进程启动、Runner panic 和未分类基础设施故障会终止 Agent Run。
