# Permission Policy Semantics

## Scope

The `permission` package defines process-local decisions for Tool effects. It does not authenticate users, load tenant roles, query a database, run approval workflows, or sandbox operating-system processes. Enterprise applications may adapt their authorization system to `permission.Policy` without exposing Enterprise types to Core.

Permission checks and resource containment are separate controls:

- `permission.Policy` decides whether an operation may proceed;
- `safespace.SafeSpace` confines local file paths to one Root;
- `command.Runner` owns command execution behavior; `builtin.bash` adds permission, timeout, and result limits.

Allowing `execute` does not make a command safe, and denying an operation does not replace resource isolation. The provided `command/local` Runner is not a sandbox: its Root constrains only the initial working directory.

## Request and Actions

Each decision receives a validated `permission.Request` containing:

- `Tool`: the stable Tool name;
- `Action`: one of `read`, `write`, `edit`, `search`, or `execute`;
- `Target`: an optional path or working directory.

`Target` must not contain credentials or full command arguments. Permission errors intentionally omit it from their message, while retaining the structured Request for controlled diagnostics.

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

`ActionPolicy` is immutable after construction and safe for concurrent checks. `AllowAll` is an explicit opt-in. `DenyAll`, an empty `ActionPolicy`, and a nil Policy deny access.

## Failure Semantics

`permission.Check` validates before invoking a Policy and returns stable error codes:

- `permission_denied`: a normal authorization denial;
- `invalid_permission_request`: invalid Core or Tool integration input;
- `permission_policy_failed`: Policy error, panic, or invalid Decision.

Cancellation is returned directly and takes precedence over a Policy result. A Policy must return promptly when its Context is canceled. `Check` contains panics but cannot preempt a blocked Policy.

All current built-in tools translate `permission_denied` into a safe, non-retryable `tool.Error`. `builtin.bash` requests `execute` for the canonical working directory and never includes the script in `permission.Request`. Invalid requests and Policy failures are integration failures and terminate the Tool call as `tool.RuntimeError`; they are never presented as an ordinary denial.

Core does not install a global Policy and does not cache decisions. The caller owns the Policy and any resources it uses.
