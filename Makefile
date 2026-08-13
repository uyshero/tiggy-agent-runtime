.PHONY: test test-race fuzz benchmark vet vuln fmt-check boundaries licenses api-compat eval-reliability eval-provider-reliability provider-contract verify mcp-compat provider-live release-audit release-verify

GOCACHE_DIR ?= $(CURDIR)/.gocache
FUZZ_TIME ?= 10s

test:
	GOCACHE="$(GOCACHE_DIR)" go test ./...

test-race:
	GOCACHE="$(GOCACHE_DIR)" go test -race ./...

fuzz:
	GOCACHE="$(GOCACHE_DIR)" go test ./internal/strictjson -run '^$$' -fuzz '^FuzzDecode$$' -fuzztime="$(FUZZ_TIME)"
	GOCACHE="$(GOCACHE_DIR)" go test ./orchestrator -run '^$$' -fuzz '^FuzzReadBrief$$' -fuzztime="$(FUZZ_TIME)"
	GOCACHE="$(GOCACHE_DIR)" go test ./artifact -run '^$$' -fuzz '^FuzzDecodeToolResult$$' -fuzztime="$(FUZZ_TIME)"
	GOCACHE="$(GOCACHE_DIR)" go test ./provider/openai -run '^$$' -fuzz '^FuzzDecodeStream$$' -fuzztime="$(FUZZ_TIME)"

benchmark:
	GOCACHE="$(GOCACHE_DIR)" go test ./orchestrator ./observe -run '^$$' -bench . -benchmem

vet:
	GOCACHE="$(GOCACHE_DIR)" go vet ./...

vuln:
	GOCACHE="$(GOCACHE_DIR)" go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "run gofmt on the files above" >&2; exit 1)

boundaries:
	sh scripts/check-boundaries.sh
	sh scripts/test-boundaries.sh

licenses:
	sh scripts/check-licenses.sh

api-compat:
	sh scripts/check-api-compatibility.sh \
		$(if $(API_BASE_VERSION),--base "$(API_BASE_VERSION)") \
		$(if $(RELEASE_VERSION),--release "$(RELEASE_VERSION)") \
		$(if $(filter 1 true yes,$(ALLOW_BREAKING_API)),--allow-incompatible)

verify: fmt-check boundaries licenses api-compat vet test test-race eval-reliability provider-contract

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

# Metadata and repository checks for a release candidate. Set RELEASE_VERSION
# to also require that exact tag at HEAD and compare API with the prior tag.
release-audit:
	sh scripts/check-release-readiness.sh $(if $(RELEASE_VERSION),--release "$(RELEASE_VERSION)")

release-verify: verify vuln release-audit

# Opt-in: downloads pinned official TypeScript reference servers through npx.
mcp-compat:
	GOCACHE="$(GOCACHE_DIR)" go test -count=1 -tags=mcpcompat -timeout=5m ./mcp

# Opt-in: performs one billed network call using the adjacent TMA dotenv by default.
provider-live:
	TMA_ENV_FILE="$(TMA_ENV_FILE)" sh scripts/verify-live-provider.sh
