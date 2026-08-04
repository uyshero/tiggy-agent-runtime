# SafeSpace Semantics

## Boundary

`safespace.OpenSafeSpace` uses Go `os.Root` to open a caller-selected directory. Every path is relative to that Root. Absolute paths, `..` traversal, and symlinks pointing outside the Root fail.

`safespace.NormalizePath` exposes the same relative-path normalization for built-in tools and injected FileSystem adapters, so permission decisions and actual I/O use one canonical Target.

```go
safeSpace, err := safespace.OpenSafeSpace("./agent-work", safespace.DefaultLimits())
if err != nil {
    return err
}
defer safeSpace.Close()
```

SafeSpace is an in-process file execution environment, not a tenant Workspace, object store, or database record. The caller owns its lifecycle; Agent and Toolkit do not close an injected FileSystem.

## Operations

- `ReadFile` accepts regular files only and checks both stat size and a streaming read limit.
- `WriteFile` enforces a write limit, creates parent directories, writes a same-directory temporary file, and renames it into place.
- `List` requires a directory, reads one level, limits entry count, and sorts by name.
- Operations check Context before work and before returning; standard filesystem calls themselves are not preemptible.

Defaults are 4 MiB per file read/write and 10,000 entries per List. Production applications should reduce them for each Agent use case.

## Atomicity and Durability

Writes use a same-directory temporary file and Rename so readers do not observe partial content. Cancellation before Rename cleans the temporary file and leaves the target unchanged.

This is not a durable write. The implementation does not perform the complete file and parent-directory `fsync` protocol and does not promise persistence after process crash, system crash, or power loss. Enterprise workflows requiring persistence guarantees should use an explicit Artifact or Storage Adapter rather than treating SafeSpace as a database.

## Security Limits

`os.Root` prevents ordinary path and symlink escape, but it does not prohibit filesystem boundary crossings, Linux bind mounts, mapped `/proc` trees, or device files already present inside the Root. The regular-file read check reduces device risk, but callers must still provide a dedicated least-privilege directory without dangerous mounts.

Security under an adversarial process continuously modifying the directory also depends on the operating system implementation. Sandboxing, containers, and command execution are separate future boundaries; SafeSpace itself is not a sandbox.
