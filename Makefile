.PHONY: test test-race vet vuln fmt-check boundaries licenses eval-reliability eval-provider-reliability provider-contract verify mcp-compat provider-live release-audit release-verify

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

licenses:
	sh scripts/check-licenses.sh

verify: fmt-check boundaries licenses vet test test-race eval-reliability provider-contract

# Runs deterministic Runtime fault scenarios and writes CI-friendly reports.
eval-reliability:
	GOCACHE="$(GOCACHE_DIR)" go run ./cmd/tiggy-runtime-eval \
		-json reports/reliability-baseline.json \
		-markdown reports/reliability-baseline.md

# Exercises deterministic HTTP/SSE Provider failures without external network access.
eval-provider-reliability:
	TIGGY_RUN_LIVE_PROVIDER_TESTS=0 GOCACHE="$(GOCACHE_DIR)" \
		go test -tags=liveprovider -run '^TestOpenAICompatibleReliabilityFixtures$$' -count=1 -v ./integration/liveprovider

# Compiles the opt-in live Adapter and runs local fixtures with network access disabled.
provider-contract:
	TIGGY_RUN_LIVE_PROVIDER_TESTS=0 GOCACHE="$(GOCACHE_DIR)" go test -tags=liveprovider ./integration/liveprovider

# Metadata and repository checks for a release candidate. Set
# RELEASE_VERSION=v0.2.1 to also require that exact tag at HEAD.
release-audit:
	sh scripts/check-release-readiness.sh $(if $(RELEASE_VERSION),--release "$(RELEASE_VERSION)")

release-verify: verify vuln release-audit

# Opt-in: downloads pinned official TypeScript reference servers through npx.
mcp-compat:
	GOCACHE="$(GOCACHE_DIR)" go test -count=1 -tags=mcpcompat -timeout=5m ./mcp

# Opt-in: performs one billed network call using the adjacent TMA dotenv by default.
provider-live:
	TMA_ENV_FILE="$(TMA_ENV_FILE)" sh scripts/verify-live-provider.sh
