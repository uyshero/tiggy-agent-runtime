#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
env_file=${TMA_ENV_FILE:-"$repo_root/../tiggy-manage-agent/.env"}

if [ ! -f "$env_file" ]; then
  echo "TMA environment file not found: $env_file" >&2
  echo "set TMA_ENV_FILE to the intended dotenv file" >&2
  exit 1
fi

echo "Running one billed live Provider contract call"
TIGGY_RUN_LIVE_PROVIDER_TESTS=1 \
TIGGY_LIVE_PROVIDER_ENV_FILE="$env_file" \
GOCACHE="${GOCACHE:-$repo_root/.gocache}" \
  go test -tags=liveprovider -run '^TestOpenAICompatibleLiveProvider$' -count=1 -v ./integration/liveprovider
