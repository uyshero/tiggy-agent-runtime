# Release Readiness

## Purpose

This document separates technical verification from authorization to publish. The Runtime is still pre-release and is not claimed to be production-ready. A passing command proves only the checks named here; it does not approve repository ownership, operational security-response capacity, or public distribution.

## Current Technical Baseline

The following gates are implemented:

- `make verify` checks formatting, Core/Enterprise dependency boundaries, the reviewed dependency-license manifest, `go vet`, unit tests, and race detection;
- normal CI runs `make verify` and the pinned `make vuln` vulnerability scan;
- tests use deterministic local doubles and require no database, real model, or network;
- `make mcp-compat` is scheduled and opt-in because it downloads pinned Node.js reference servers;
- the module has no neighboring-repository `replace` directive and Core has no Enterprise or database dependency;
- public protocol changes are recorded in dated release sections or accumulated under `Unreleased` after a release baseline.

These results are an engineering baseline, not a production-readiness claim. Deployment-specific provider adapters, persistent Memory/Artifact adapters, credentials, isolation, retention, SLOs, and incident operations belong to the embedding application or Enterprise service.

## Current Blockers

The repository contains local `v0.1.0` and `v0.1.1` release tags. The audit does not prove that either tag is publicly available. The `v0.1.1` release is technically prepared locally, but public distribution remains blocked by owner-controlled decisions. Changes after that local baseline are accumulated under `Unreleased` as the `v0.1.2` candidate and are not a published release.

- availability and ownership of the public module path `github.com/uyshero/tiggy-agent-runtime` still require owner confirmation;
- delivery and monitoring of `security@tiggy.cloud` must be operationally confirmed;
- an authorized owner has not yet approved pushing the tag or publishing the release.

Apache-2.0, the private security-reporting address, and the `v0.1.0`/`v0.1.1` dates are recorded. Do not resolve remaining items by inventing ownership, operational validation, or publication approval.

## Owner Decisions

Recorded decisions:

- project license: Apache-2.0;
- first tagged baseline: `v0.1.0`, dated 2026-08-04;
- latest local release baseline: `v0.1.1`, dated 2026-08-05;
- current unreleased candidate: `v0.1.2`;
- security policy: latest `0.1.x`, private reports to `security@tiggy.cloud`.

Before publication, the repository owner must still record:

1. the legal owner and whether the GitHub organization/module path are final and publicly available;
2. who may approve, tag, publish, revoke, and respond to security reports;
3. explicit authorization for the publication action.

## Automated Audit

Run metadata checks without changing repository state:

```bash
make release-audit
```

The audit reports all detectable blockers and is intentionally separate from `make verify`. It checks Git history and cleanliness, `LICENSE`, the exact module path, absence of `replace` directives, required security-policy structure, and a dated changelog release section.

For a final candidate, require a version-specific changelog section and the exact tag at `HEAD`:

```bash
make release-verify RELEASE_VERSION=v0.1.1
```

The expected changelog heading is `## [0.1.1] - YYYY-MM-DD`. The command does not create commits or tags and does not test public repository availability. A clean, committed release candidate is required, so the audit is expected to fail in an uncommitted extraction workspace.

## Current Release Checklist

- [ ] owner and repository ownership are recorded;
- [x] Apache-2.0 is approved and `LICENSE` is added;
- [x] dependency licenses are reviewed and an allow/deny policy is enforced in CI;
- [ ] public module path ownership and repository availability are confirmed;
- [x] `SECURITY.md` contains the required supported-version, reporting, and contact contract;
- [x] the local `v0.1.0` tag points at its release commit;
- [x] `v0.1.1` baseline changes are assigned to `v0.1.1`;
- [x] post-`v0.1.1` changes are recorded under `Unreleased` as the `v0.1.2` candidate;
- [x] the `v0.1.0` baseline is recorded under `## [0.1.0] - 2026-08-04`;
- [x] `v0.1.1` is recorded under `## [0.1.1] - 2026-08-05`;
- [x] `make release-verify RELEASE_VERSION=v0.1.1` passes at the tagged commit;
- [ ] scheduled MCP compatibility is green for the pinned reference-server versions;
- [ ] release notes state pre-v1 compatibility, supported Go version, known limitations, and rollback or revocation ownership;
- [ ] an authorized owner explicitly approves pushing the tag and publishing the release.

Publishing remains a separate human-controlled action after this checklist passes.
