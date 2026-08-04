# Permission Policy 语义

## 边界

`permission` 包只负责 Tool 外部效果的进程内决策。它不认证用户、不加载租户角色、不查询数据库、不执行审批流程，也不隔离操作系统进程。企业应用可以把自身授权系统适配为 `permission.Policy`，但不能让企业类型进入 Core。

权限判断和资源隔离是两个独立控制：

- `permission.Policy` 判断操作是否允许；
- `safespace.SafeSpace` 把本地文件路径限制在一个 Root 内；
- `command.Runner` 负责命令执行行为，`builtin.bash` 叠加权限、超时和结果上限。

允许 `execute` 不代表命令是安全的；拒绝操作也不能替代资源隔离。内置 `command/local` Runner 不是沙箱，它的 Root 只约束初始工作目录。

## Request 与 Action

每次决策接收一个已校验的 `permission.Request`：

- `Tool`：稳定的 Tool 名称；
- `Action`：`read`、`write`、`edit`、`search` 或 `execute`；
- `Target`：可选路径或工作目录。

`Target` 不能包含凭证或完整命令参数。Permission Error 的文本有意不输出 Target，但会保留结构化 Request，供受控诊断使用。

```go
policy, err := permission.NewActionPolicy(
    permission.ActionRead,
    permission.ActionSearch,
)
if err != nil {
    return err
}

err = permission.Check(ctx, policy, permission.Request{
    Tool:   "builtin.read",
    Action: permission.ActionRead,
    Target: "notes/today.txt",
})
```

`ActionPolicy` 构造后不可变，可以并发调用。`AllowAll` 必须显式启用；`DenyAll`、空 `ActionPolicy` 和 nil Policy 都默认拒绝。

## 失败语义

`permission.Check` 先校验 Request，再调用 Policy，并返回稳定错误码：

- `permission_denied`：正常权限拒绝；
- `invalid_permission_request`：Core 或 Tool 集成输入无效；
- `permission_policy_failed`：Policy 返回错误、发生 panic 或返回非法 Decision。

Context 取消直接返回，并优先于 Policy 结果。Policy 必须在 Context 取消后及时返回；`Check` 可以隔离 panic，但无法抢占永久阻塞的 Policy。

当前全部内置 Tool 都把 `permission_denied` 转成安全且不可重试的 `tool.Error`。`builtin.bash` 针对规范化工作目录请求 `execute`，且永远不把脚本放入 `permission.Request`。非法 Request 和 Policy 故障属于集成错误，以 `tool.RuntimeError` 终止 Tool 调用，不能伪装成普通权限拒绝。

Core 不安装全局 Policy，也不缓存 Decision。调用方拥有 Policy 及其使用的资源。
