# Contributing

Read these documents before changing public contracts:

- [Architecture](./docs/architecture.md)
- [Dependency policy](./docs/dependencies.md)
- [Engineering standards](./docs/standards.md)
- [Roadmap](./docs/roadmap.md)

## Local Verification

```bash
make verify
```

## Pull Requests

Keep changes narrowly scoped. A public API change must explain compatibility, event/error semantics, cancellation, resource ownership, and failure-path tests. A new dependency must complete the admission checklist in `docs/dependencies.md`.

Do not add Enterprise database, HTTP, Worker, tenant, or OpenAPI client code to this repository.

## Commit Style

Use conventional prefixes where practical:

```text
feat: add runtime event sequencer
fix: preserve tool result ordering on cancellation
docs: define memory scope contract
test: inject model stream failure
```

## Formatting

Run `gofmt` on Go files. CI rejects unformatted code, failed vet checks, failed tests, data races, and dependency-boundary violations.
