# Release Readiness

## Purpose

This document separates technical verification from authorization to publish. The Runtime is still pre-release and is not claimed to be production-ready. A passing command proves only the checks named here; it does not approve ownership, licensing, security response capacity, or public distribution.

## Current Technical Baseline

The following gates are implemented:

- `make verify` checks formatting, Core/Enterprise dependency boundaries, `go vet`, unit tests, and race detection;
- normal CI runs `make verify` and the pinned `make vuln` vulnerability scan;
- tests use deterministic local doubles and require no database, real model, or network;
- `make mcp-compat` is scheduled and opt-in because it downloads pinned Node.js reference servers;
- the module has no neighboring-repository `replace` directive and Core has no Enterprise or database dependency;
- public protocol changes are accumulated in `CHANGELOG.md` under `Unreleased`.

These results are an engineering baseline, not a production-readiness claim. Deployment-specific provider adapters, persistent Memory/Artifact adapters, credentials, isolation, retention, SLOs, and incident operations belong to the embedding application or Enterprise service.

## Current Blockers

The repository must not be published yet. At the time this baseline was written:

- the repository has no initial commit or reviewable history;
- no project license has been approved and no `LICENSE` file exists;
- repository ownership and public publication have not been approved;
- `SECURITY.md` has no supported-version table or monitored contact;
- the changelog has no dated release section and no version/tag baseline exists;
- license-compliance policy and CI cannot be finalized before the project license and compatible-license policy are approved;
- availability and ownership of the public module path `github.com/uyshero/tiggy-agent-runtime` have not been confirmed.

Do not resolve these items by inventing names, contacts, dates, approvals, or license terms.

## Owner Decisions

Before the first release, the repository owner must decide and record:

1. the legal owner and approved project license;
2. whether the GitHub organization and module path are final and publicly available;
3. the monitored private vulnerability-reporting channel and supported-version policy;
4. the first semantic version and release date;
5. the compatible and prohibited dependency-license policy;
6. who may approve, tag, publish, revoke, and respond to security reports.

## Automated Audit

Run metadata checks without changing repository state:

```bash
make release-audit
```

The audit reports all detectable blockers and is intentionally separate from `make verify`. It checks Git history and cleanliness, `LICENSE`, the exact module path, absence of `replace` directives, required security-policy structure, and a dated changelog release section.

For a final candidate, require a version-specific changelog section and the exact tag at `HEAD`:

```bash
make release-verify RELEASE_VERSION=v0.1.0
```

The expected changelog heading is `## [0.1.0] - YYYY-MM-DD`. The command does not create commits or tags and does not test public repository availability. A clean, committed release candidate is required, so the audit is expected to fail in an uncommitted extraction workspace.

## First-Release Checklist

- [ ] owner and repository ownership are recorded;
- [ ] project license is approved and `LICENSE` is added;
- [ ] dependency licenses are reviewed and an allow/deny policy is enforced in CI;
- [ ] public module path ownership and repository availability are confirmed;
- [ ] `SECURITY.md` contains `## Supported Versions`, `## Reporting a Vulnerability`, and a `Security contact: mailto:...` or HTTPS contact line;
- [ ] initial history is reviewed and the candidate worktree is clean;
- [ ] relevant `Unreleased` changes are moved into a dated semantic-version section;
- [ ] `make release-verify RELEASE_VERSION=vX.Y.Z` passes at the tagged commit;
- [ ] scheduled MCP compatibility is green for the pinned reference-server versions;
- [ ] release notes state pre-v1 compatibility, supported Go version, known limitations, and rollback or revocation ownership;
- [ ] an authorized owner explicitly approves publication.

Publishing remains a separate human-controlled action after this checklist passes.
