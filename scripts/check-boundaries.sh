#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

for forbidden in \
  'tiggy-manage-agent/' \
  'github.com/jackc/pgx' \
  'database/sql' \
  'internal/managedagents' \
  'internal/runner' \
  'internal/httpapi'
do
  if grep -R -n --include='*.go' --include='go.mod' "$forbidden" . \
    --exclude-dir=.git --exclude-dir=.gocache; then
    echo "forbidden core dependency: $forbidden" >&2
    exit 1
  fi
done

if find . -type f \( -name '*.sql' -o -name 'openapi.yaml' -o -name 'openapi.yml' \) \
  -not -path './.git/*' | grep -q .; then
  echo "database schemas and OpenAPI service contracts do not belong in this repository" >&2
  exit 1
fi

protocol_dependencies=$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./message ./model ./event ./permission ./safespace ./skill ./contextmanager ./artifact ./memory ./command/...)
for dependency in $protocol_dependencies; do
  case "$dependency" in
    github.com/uyshero/tiggy-agent-runtime/*) ;;
    *)
      echo "foundation package depends on third-party package: $dependency" >&2
      exit 1
      ;;
  esac
done

direct_dependencies=$(go list -m -f '{{if and (not .Main) (not .Indirect)}}{{.Path}}{{end}}' all)
for dependency in $direct_dependencies; do
  case "$dependency" in
    github.com/bmatcuk/doublestar/v4) ;;
    github.com/modelcontextprotocol/go-sdk) ;;
    github.com/santhosh-tekuri/jsonschema/v6) ;;
    go.opentelemetry.io/otel) ;;
    go.opentelemetry.io/otel/metric) ;;
    go.opentelemetry.io/otel/sdk) ;;
    go.opentelemetry.io/otel/sdk/metric) ;;
    go.opentelemetry.io/otel/trace) ;;
    *)
      echo "direct dependency is not approved: $dependency" >&2
      exit 1
      ;;
  esac
done

echo "repository dependency boundaries are valid"
