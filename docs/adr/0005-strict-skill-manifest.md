# ADR 0005: Strict Skill Manifest and Explicit Activation

- Status: Accepted
- Date: 2026-08-03

## Context

Skills need a portable instruction/resource boundary without becoming a package manager, code plugin system, or hidden capability escalator. Existing `SKILL.md` ecosystems use differing YAML frontmatter fields and do not uniformly require versions, bounded resources, or typed Toolkit activation. Core needs deterministic parsing, clear authority, cancellation, content limits, and provenance while retaining zero new dependencies for this capability.

## Decision

1. Tiggy Skills use a strict `skill.json` manifest with schema identifier `tiggy.skill/v1`; Markdown instructions remain a separate declared file, commonly `SKILL.md`.
2. Name, version, description, instruction path, Tool Groups, and Resources are explicit. Unknown fields, duplicate JSON keys, duplicate identifiers/paths, non-canonical paths, and directory/name mismatches fail discovery.
3. Discovery receives a caller-owned `safespace.Reader`, scans one directory level, reads manifests only, and publishes no partial Catalog after an error.
4. Activation explicitly selects Skills and individual declared Resources. All selections are validated before content I/O. Activated UTF-8 content is bounded, copied, and hashed into an immutable snapshot.
5. Active Skill instructions have System authority and are injected only when a new Agent State is created. Skill Tool Groups are unioned with Agent-configured Groups. An active instruction-only Skill exposes no Toolkit Tools by default.
6. `run.started` records Skill name, version, digest, and effective Tool Groups without recording instruction/resource bodies.
7. Core does not discover global paths, read environment configuration, download remote Skills, run hooks, auto-select Skills through the model, or dynamically register Tools.
8. External Skill formats may be supported later by explicit adapters that produce the Tiggy V1 contract. Core does not claim format compatibility without such an adapter and compatibility suite.

## Consequences

- Parsing and validation use only the Go standard library; `skill` depends on the existing standard-library-only `safespace` contract and adds no module dependency.
- JSON is more verbose than YAML frontmatter, but it allows strict unknown-field and duplicate-key rejection without introducing a parser dependency or ambiguous scalar behavior.
- Application code controls which trusted content receives System authority and which Tool Groups become visible.
- Catalog metadata is snapshotted at discovery; selected content is snapshotted at activation. Applications explicitly rediscover/reactivate to accept file changes.
- The declared version is an opaque provenance value. Version ordering, signature validation, distribution, approval, and tenant policy remain application or Enterprise responsibilities.

## Rejected Alternatives

- Parse YAML frontmatter locally with ad hoc string logic: unsafe around multiline values, duplicate keys, escaping, and evolving dialects.
- Add a YAML dependency and accept arbitrary `SKILL.md` formats: creates a false compatibility promise without one stable schema.
- Load every Resource during discovery: wastes I/O and memory and violates explicit resource activation.
- Let the model discover and activate Skills dynamically in Core: hides authority changes inside model output and requires a separate approval/context policy.
- Treat Skills as executable plugins: duplicates Tool/MCP extension boundaries and expands code-execution risk.
