# ADR 0003: Doublestar Glob Pattern Matching

- Status: Accepted
- Date: 2026-08-03

## Context

`builtin.glob` and the Grep include filter need familiar `**`, wildcard, character-class, and alternative matching over canonical slash-separated paths. Go `path.Match` does not support recursive doublestar semantics. A local parser would create avoidable compatibility, escaping, denial-of-service, and maintenance risk inside a security-sensitive filesystem boundary.

The runtime must retain control of traversal. A pattern library must not receive the host filesystem, follow symlinks, start goroutines, or expose its types through the public Core API.

## Decision

1. Built-in search uses `github.com/bmatcuk/doublestar/v4` at `v4.10.0`.
2. Core calls only `ValidatePattern` and `MatchUnvalidated` against already canonicalized relative paths.
3. Doublestar does not perform filesystem I/O. Tiggy owns traversal, entry/depth/result/output limits, cancellation checks, deterministic ordering, and FileSystem error handling.
4. Patterns use forward-slash separators and are limited to 4096 bytes.
5. Third-party types remain private to `builtin`; Tool inputs and outputs use Tiggy-owned values.

## Dependency Assessment

- Implemented capability: recursive Glob and Grep include-pattern matching.
- Standard-library alternative: `path.Match` lacks `**`; composing it locally would require a new parser and compatibility surface.
- Version and maintenance: `v4.10.0` was released on 2026-01-25; the project has maintained the same focused capability since 2014.
- License: MIT.
- Minimum dependency Go version: 1.16; Runtime remains on Go 1.25.12.
- Transitive dependencies: none.
- CGO: none.
- Owned goroutines, files, or network resources in selected APIs: none.
- Public API exposure: none.
- Security: `govulncheck v1.6.0` reported no reachable vulnerabilities with Go 1.25.12 on 2026-08-03.
- Replacement path: replace the private validation/match calls and rerun built-in pattern, traversal-limit, and compatibility tests.

## Consequences

- Users receive established doublestar semantics without granting a third party filesystem access.
- Traversal behavior and resource bounds remain a Tiggy contract independent of library filesystem helpers.
- The runtime adds one direct dependency and a small binary-size cost.
- Compatibility tests must cover patterns relied on by Tool prompts before changing major versions.

## Rejected Alternatives

- `path.Match` plus a recursive boolean: does not provide common `**` behavior and diverges from expected Glob syntax.
- Hand-written doublestar parser: unnecessary correctness and security surface.
- `doublestar.Glob` over an `fs.FS`: would couple third-party traversal semantics to SafeSpace limits, cancellation, error classification, and symlink handling.
- Shell expansion: platform-dependent, unsafe, and incompatible with an embeddable runtime.
