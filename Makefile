.PHONY: test test-race vet vuln fmt-check boundaries verify mcp-compat release-audit release-verify

GOCACHE_DIR ?= $(CURDIR)/.gocache

test:
	GOCACHE="$(GOCACHE_DIR)" go test ./...

test-race:
	GOCACHE="$(GOCACHE_DIR)" go test -race ./...

vet:
	GOCACHE="$(GOCACHE_DIR)" go vet ./...

vuln:
	GOCACHE="$(GOCACHE_DIR)" go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "run gofmt on the files above" >&2; exit 1)

boundaries:
	sh scripts/check-boundaries.sh

verify: fmt-check boundaries vet test test-race

# Metadata and repository checks for a release candidate. Set
# RELEASE_VERSION=v0.1.0 to also require that exact tag at HEAD.
release-audit:
	sh scripts/check-release-readiness.sh $(if $(RELEASE_VERSION),--release "$(RELEASE_VERSION)")

release-verify: verify vuln release-audit

# Opt-in: downloads pinned official TypeScript reference servers through npx.
mcp-compat:
	GOCACHE="$(GOCACHE_DIR)" go test -count=1 -tags=mcpcompat -timeout=5m ./mcp
