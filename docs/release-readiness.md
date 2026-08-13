# Release Readiness

## Purpose

This document separates technical verification from authorization to publish. The Runtime is still pre-release and is not claimed to be production-ready. A passing command proves only the checks named here; it does not approve repository ownership, operational security-response capacity, or public distribution.

## Current Technical Baseline

The following gates are implemented:

- `make verify` checks formatting, Core/Enterprise dependency boundaries, the reviewed dependency-license manifest, `go vet`, unit tests, and race detection;
- deterministic fuzz seeds run in normal package tests; `make fuzz` and `make benchmark` are opt-in diagnostics for parser robustness and allocation/throughput baselines;
- normal CI runs `make verify` and the pinned `make vuln` vulnerability scan;
- tests use deterministic local doubles and require no database, real model, or network;
- `make mcp-compat` is scheduled and opt-in because it downloads pinned Node.js reference servers;
- the module has no neighboring-repository `replace` directive and Core has no Enterprise or database dependency;
- public protocol changes are recorded in dated release sections or accumulated under `Unreleased` after a release baseline.

These results are an engineering baseline, not a production-readiness claim. Deployment-specific provider adapters, persistent Memory/Artifact adapters, credentials, isolation, retention, SLOs, and incident operations belong to the embedding application or Enterprise service.

## Current Blockers

The `v0.2.3` release records the hardened Sequential Brief Orchestrator event
and JSON contracts. This records the release baseline, but it does not establish
legal ownership or operational security-response readiness.

- availability and ownership of the public module path `github.com/uyshero/tiggy-agent-runtime` still require owner confirmation;
- delivery and monitoring of `security@tiggy.cloud` must be operationally confirmed;
- the remaining organization-level publication and security-response roles are not documented in this repository.

Apache-2.0, the private security-reporting address, and the release dates through
`v0.2.3` are recorded. The workspace owner explicitly authorized releasing
`v0.2.3` on 2026-08-13. This does not confirm the unresolved legal-ownership or
security-operations items above.

## Owner Decisions

Recorded decisions:

- project license: Apache-2.0;
- first tagged baseline: `v0.1.0`, dated 2026-08-04;
- latest published release baseline: `v0.2.3`, dated 2026-08-13;
- no later release candidate is currently recorded; new work remains under `Unreleased`;
- `v0.2.3` publication was authorized by the workspace owner on 2026-08-13;
- security policy after release: latest `0.2.x`, private reports to `security@tiggy.cloud`.

Before a future publication, the repository owner must still record:

1. the legal owner and whether the GitHub organization/module path are final and publicly available;
2. who may approve, tag, publish, revoke, and respond to security reports;
3. explicit authorization for the publication action.

## Automated Audit

Run metadata checks without changing repository state:

```bash
make release-audit
```

The audit reports all detectable blockers and is intentionally separate from
`make verify`. It checks Git history and cleanliness, `LICENSE`, the exact
module path, absence of `replace` directives, required security-policy
structure, and a dated changelog release section. `make verify` separately runs
`make api-compat` against the latest reachable stable tag.

For a final candidate, require a version-specific changelog section and the exact tag at `HEAD`:

```bash
make release-verify RELEASE_VERSION=v0.2.3
```

The expected changelog heading is `## [0.2.3] - YYYY-MM-DD`. The command does not create commits or tags and does not test public repository availability. A clean, committed release candidate is required, so the audit is expected to fail in an uncommitted extraction workspace.

## Current Release Checklist

- [ ] owner and repository ownership are recorded;
- [x] Apache-2.0 is approved and `LICENSE` is added;
- [x] dependency licenses are reviewed and an allow/deny policy is enforced in CI;
- [ ] public module path ownership and repository availability are confirmed;
- [x] `SECURITY.md` contains the required supported-version, reporting, and contact contract;
- [x] the local `v0.1.0` tag points at its release commit;
- [x] the `v0.2.3` release contents are assigned to a dated changelog section;
- [x] Orchestrator changes are assigned to the dated `v0.2.2` release section;
- [x] the `v0.1.0` baseline is recorded under `## [0.1.0] - 2026-08-04`;
- [x] `v0.1.1` is recorded under `## [0.1.1] - 2026-08-05`;
- [x] `v0.1.2` is recorded under `## [0.1.2] - 2026-08-11`;
- [x] `v0.2.0` is recorded under `## [0.2.0] - 2026-08-11`;
- [x] `v0.2.1` is recorded under `## [0.2.1] - 2026-08-12`;
- [x] `v0.2.2` is recorded under `## [0.2.2] - 2026-08-13`;
- [x] `v0.2.3` is recorded under `## [0.2.3] - 2026-08-13`;
- [x] `make release-verify RELEASE_VERSION=v0.2.3` passes at the tagged commit;
- [ ] scheduled MCP compatibility is green for the pinned reference-server versions;
- [x] release notes state pre-v1 compatibility, supported Go version, known limitations, and rollback or revocation ownership;
- [x] the workspace owner explicitly approved publishing `v0.2.3` on 2026-08-13.

Publishing any later version remains a separate human-controlled action.
