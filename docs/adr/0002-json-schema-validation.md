# ADR 0002: JSON Schema Validation for Tool Arguments

- Status: Accepted
- Date: 2026-08-03

## Context

Checking only that Tool arguments are a JSON object does not enforce required fields, types, bounds, enums, or nested structure. Implementing JSON Schema locally would create a large compatibility and security surface for an established standard.

Tool schemas are untrusted extension input. Schema compilation must not perform implicit network or filesystem I/O, and third-party types must not enter the public Core API.

## Decision

1. Toolkit uses `github.com/santhosh-tekuri/jsonschema/v6` at `v6.0.2` internally.
2. Schemas without an explicit dialect use JSON Schema Draft 2020-12.
3. Format assertions are enabled; content assertions remain disabled.
4. Input schemas compile once during Tool registration. Invalid schemas reject registration.
5. Runtime arguments decode with JSON number preservation and validate before Tool execution.
6. Validation failures become safe, model-actionable `tool.Error` values with code `invalid_tool_arguments`; detailed validation errors remain in the Cause.
7. External schema loading is disabled. Local fragments and `$defs` work, while file and remote `$ref` values fail registration.
8. Compiled schema types remain private to `toolkit`; public APIs continue to expose Tiggy values and `json.RawMessage` only.
9. Output Schema enforcement is deferred until Core defines an unambiguous structured Tool output contract.

## Dependency Assessment

- License: Apache-2.0.
- Transitive license: `golang.org/x/text` uses a BSD-style license.
- Minimum library Go version: 1.21. The module baseline is Go 1.25.12 because vulnerability scanning found reachable `net/url` and `os.Root` issues in earlier Go 1.25 patch releases.
- The Schema library adds one transitive module, `golang.org/x/text`; the current Runtime graph selects security-fixed `v0.39.0`.
- Maintenance: `v6.0.2` was released in May 2025 and the project publishes compliance results against the JSON Schema Test Suite.
- Security: `govulncheck v1.6.0` reported no reachable vulnerabilities with Go 1.25.12 on 2026-08-03. CI must repeat the scan before public release.
- Binary size: the compiler and Unicode message support increase Toolkit-linked binaries; this is accepted for standards-compliant validation and must remain visible in release artifact comparisons.
- CGO: none.
- Owned goroutines, network connections, or files: none in validation. Toolkit replaces the default resource loader so compilation cannot perform external I/O.
- Public API exposure: none.
- Replacement path: replace the private compiler/validator functions in `toolkit` and rerun the protocol and behavior suite.

## Consequences

- Tool argument correctness no longer depends on each Tool repeating schema checks.
- Registration is slightly more expensive; calls reuse immutable compiled schemas.
- Unsupported or external references fail early instead of being resolved dynamically.
- The module is no longer standard-library-only, but protocol packages remain standard-library-only.

## Rejected Alternatives

- Hand-written validation: incomplete draft support and high long-term correctness cost.
- Validate inside every Tool: duplicated behavior and inconsistent safe errors.
- Remote `$ref` resolution: hidden I/O, availability risk, and a schema supply-chain boundary inside Agent construction.
- Provider-only strict schema enforcement: not all providers enforce schemas, and adapters can still return malformed calls.
