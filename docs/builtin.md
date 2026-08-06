# Built-in Tool Semantics

## Construction and Ownership

Built-in tools are ordinary `tool.Tool` implementations. Applications construct them with explicit dependencies and register them in Toolkit:

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
todo, err := builtin.NewTodo(builtin.TodoOptions{})
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
kit, err := toolkit.New(read, write, edit, todo, glob, grep, bash)
```

Tool names are stable: `builtin.read`, `builtin.write`, `builtin.edit`, `builtin.todo`, `builtin.glob`, `builtin.grep`, and `builtin.bash`. All currently declare sequential execution. The caller owns FileSystem, Policy, and Runner lifecycles; tools do not close injected dependencies.

A nil Policy denies all calls. Applications must opt in to `permission.AllowAll` explicitly. Bash also has no default Runner: enabling local process execution requires explicitly importing and constructing `command/local`, as shown above. Shell paths are platform configuration, not a portable Core default.

## Path and Permission Order

Each Tool validates JSON arguments and normalizes the path with `safespace.NormalizePath` before permission evaluation. Permission Policy and FileSystem therefore receive the same relative, cleaned Target. Absolute paths, parent escape, Root-as-file, NUL bytes, and paths over 4096 bytes are rejected before external effects.

The operation order is:

```text
JSON Schema and argument validation
  -> canonical relative path
  -> content/output limit validation where possible
  -> Permission Policy
  -> FileSystem operation
  -> bounded Tool Result
```

Read requests `permission.ActionRead`, Write requests `permission.ActionWrite`, Edit requests `permission.ActionEdit`, and Glob/Grep request `permission.ActionSearch`.

Bash requests `permission.ActionExecute` with only the canonical working directory as Target. The full script is passed to Runner after authorization and is never placed in `permission.Request`.

Recursive search authorizes the canonical search Root once. A FileSystem adapter that requires finer object-level authorization must enforce it during List and ReadFile; Core does not issue an external Policy call for every discovered entry.

## Read

Read accepts `{"path":"..."}` and returns UTF-8 text. Its default model-visible file-content limit is 128 KiB and can be reduced or explicitly increased through `ReadOptions.MaxBytes`. The FileSystem must still enforce its own input/resource limits because Read can only check the returned buffer.

Non-empty files return one summary Content block followed by one file-content block. Empty files return a non-empty summary so the Tool Result remains protocol-valid. Binary or otherwise invalid UTF-8 content returns `unsupported_file_encoding`; Read does not silently transcode or place binary data into model context.

The summary includes a `file_digest=sha256:...` marker, and `Result.State` carries a machine-readable `path` / `size_bytes` / `digest` snapshot so callers can feed the exact digest back into `builtin.edit` as `expected_digest`.

## Write

Write accepts `{"path":"...","content":"..."}` and creates or replaces a file with mode `0644`. Its default content limit is 1 MiB and can be changed through `WriteOptions.MaxBytes`. Content size is checked before permission evaluation and I/O.

SafeSpace currently implements replacement with a same-directory temporary file and Rename. Another FileSystem implementation may provide different atomicity and durability guarantees. Write itself does not promise durable persistence, append semantics, conflict detection, or exactly-once effects.

## Edit

Edit accepts `path`, non-empty `old_text`, `new_text`, optional `replace_all`, optional `expected_digest`, and optional `dry_run`. By default, `old_text` must occur exactly once: zero matches return `text_not_found`, and multiple matches return `ambiguous_match`. Setting `replace_all=true` replaces every non-overlapping exact match.

Edit reads and writes at most 1 MiB by default, requires UTF-8, and writes mode `0644`. It performs no write when replacement leaves content unchanged. If `expected_digest` is supplied and the current file digest differs, the Tool returns `file_changed` instead of writing. If `dry_run=true`, the Tool returns the planned change and digests without writing.

Edit is a read-modify-write operation over `FileSystem`; it has no compare-and-swap contract and cannot detect a concurrent writer between ReadFile and WriteFile unless callers supply `expected_digest`. SafeSpace replacement atomicity prevents partial content visibility but does not prevent lost updates or promise durability.

## Todo

Todo accepts a bounded run-local checklist snapshot and returns the updated snapshot in `Result.State`. It is not a project-management backend or durable task queue. The tool is useful when the model needs to keep a short structured list of tasks, mark them done, remove items, or clear the checklist inside one Agent Run.

The input accepts an optional `state` snapshot, so the model can thread the prior tool result back into a later call. Supported actions are `list`, `add`, `update`, `complete`, `remove`, and `clear`. The returned text includes the current checklist, while `Result.State` carries the machine-readable snapshot with `version`, `next_id`, and `items`.

Applications and tests can call `builtin.ExtractLatestTodoState(messages)` to recover the latest successful Todo snapshot from Agent history. This helper only reads Tool Result state and validates the snapshot; it does not persist, merge, or schedule tasks.

Todo items are bounded by count and title size. It is intended as a small execution aid, not as a database-backed task system.

## Glob and Grep

Glob and Grep recursively traverse from optional `path` (default Root) under shared `SearchLimits`:

```text
entries       10,000
results        1,000
depth             64
result text   128 KiB
file read       1 MiB
```

Zero-valued fields use these defaults. Entry or depth exhaustion returns `search_limit_exceeded`. Result-count or output-byte exhaustion returns an explicitly truncated partial result. Traversal sorts every directory by Entry Name and rejects malformed FileSystem Entry names.

Glob uses established doublestar patterns relative to the requested path, including `**`, character classes, and alternatives. It returns Root-relative paths; directories end in `/`. Pattern matching includes dotfiles when the pattern matches them.

Grep compiles `pattern` with Go's RE2-based `regexp` engine and matches one line at a time. Optional `include` is a doublestar file filter and defaults to `**`. Output uses quoted path, one-based line number, and text. Individual lines are truncated at 4096 bytes. Files that disappear, cannot be accessed, exceed the file limit, or are not UTF-8 are skipped and counted; unknown FileSystem failures remain fatal RuntimeErrors.

## Bash

Bash accepts required `script` and optional `path`; `path` defaults to Root. It validates and canonicalizes input, checks `execute` permission, and invokes the injected `command.Runner`. Defaults are a 30-second Tool timeout, 32 KiB script limit, and 128 KiB combined stdout/stderr result limit. Runner implementations must enforce their own limits too because a Tool cannot bound resources used inside an arbitrary Runner.

A non-zero shell exit is an ordinary Tool Result containing the exit code and any captured stdout/stderr; it is not a Runtime Failure. Output must be UTF-8. Runner directory/output errors become model-actionable errors, while Runner startup, contract, panic, or infrastructure failures terminate the Agent run as RuntimeErrors.

The optional `command/local` Runner uses `os/exec` and has the same default script and combined output limits. It does not inherit the host environment by default; it supplies a fixed `PATH`, Root as `HOME`, and a UTF-8 locale. Supplying `Environment` replaces that default exactly. Its configured Root constrains only the initial working directory. A script may still access absolute host paths, the network, credentials available through operating-system authority, and resources outside Root. Use a container, VM, remote executor, or another Runner when isolation is required.

Cancellation terminates the directly started shell through `os/exec`, but it cannot undo filesystem, network, or other effects already produced. The local adapter's `WaitDelay` bounds waiting on inherited output pipes; it does not provide process-tree isolation, and descendants may outlive the shell depending on the operating system. Neither Bash nor Runner promises exactly-once execution or rollback.

## Failures

Model-actionable, non-retryable Tool errors include:

- `permission_denied`;
- `invalid_tool_arguments`, `invalid_path`, `invalid_directory`, and `invalid_pattern`;
- `file_not_found`, `file_access_denied`, `not_regular_file`, and `not_directory`;
- `file_too_large` and `unsupported_file_encoding`;
- `text_not_found`, `ambiguous_match`, and `search_limit_exceeded`;
- `command_too_large`, `command_timeout`, `command_output_limit`, and `unsupported_command_output`.

Permission extension failures use their stable RuntimeError code. Unclassified FileSystem failures use `file_system_failed`. Invalid Runner results, startup failures, panics, and unclassified Runner failures remain RuntimeErrors. Toolkit preserves a Tool-supplied `tool.RuntimeError`; these failures terminate the Agent run rather than being shown as an ordinary denial.

Context cancellation and external deadline errors propagate directly. Neither Toolkit nor a built-in Tool can preempt a Policy, FileSystem, or Runner implementation that ignores Context.
