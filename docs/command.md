# Command Runner Semantics

## Contract

`command.Runner` is the Core Port for bounded shell execution. A Request contains a non-empty script and a canonical Root-relative working directory. A Result owns its stdout/stderr buffers and carries an exit code; `-1` means termination without a portable numeric code. Non-zero exit codes are valid Results, not Runner errors.

Implementations shared by Tool calls must be concurrency-safe, honor Context cancellation, and bound script size and captured output. Callers provide a finite deadline; stronger CPU, memory, process-count, and isolation controls depend on the selected backend. Infrastructure failures use typed `command.Error` codes. The Runner contract does not grant permission, select a shell, define a security boundary, persist execution, or promise rollback/exactly-once effects.

`builtin.bash` requires an injected Runner. This keeps host process authority out of the default Tool construction path and allows applications to provide a container, VM, remote executor, test double, or policy-specific adapter.

## Local Adapter

`command/local` is an explicit opt-in adapter implemented with standard-library `os/exec`. Construction requires an existing Root directory and an absolute executable Shell path. Both paths are resolved through symlinks. Each request directory must exist and resolve within Root before the process starts.

This containment applies only to the initial working directory. It does not restrict absolute paths, shell redirection, network access, operating-system credentials, process creation, mounts, devices, or other host resources. Do not execute untrusted scripts with this adapter unless the surrounding process is already isolated.

Defaults are:

```text
script             32 KiB
stdout + stderr   128 KiB
WaitDelay              2s
environment       fixed PATH, HOME=Root, UTF-8 locale
```

A non-nil custom Environment is copied, validated, and used exactly; the host environment is never implicitly appended. stdout and stderr are captured separately, so their cross-stream interleaving order is not represented. Output-limit exhaustion cancels the direct shell and returns the bounded partial buffers with `command_output_limit`.

The caller supplies timeout or cancellation through Context. Cancellation cannot undo effects already produced. `exec.CommandContext` targets the directly started shell, and `WaitDelay` only bounds waiting for inherited I/O pipes; neither feature guarantees process-tree termination. Applications needing stronger lifecycle or security guarantees must inject another Runner.

## Ownership and Failure Rules

- Request strings remain caller-owned immutable values.
- Result buffers returned by the local adapter are fresh copies owned by the caller.
- The Runner owns each process it creates until `Run` returns.
- Invalid Core-generated requests and impossible Runner results are integration failures.
- Invalid/unavailable working directories and output exhaustion are model-actionable in `builtin.bash`.
- Process startup, Runner panic, and unclassified infrastructure failures terminate the Agent run.
