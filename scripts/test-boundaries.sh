#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT HUP INT TERM

cp "$repo_root/go.mod" "$fixture/go.mod"
cp "$repo_root/go.sum" "$fixture/go.sum"
mkdir -p "$fixture/scripts" "$fixture/message" "$fixture/model" "$fixture/event" \
  "$fixture/permission" "$fixture/safespace" "$fixture/skill" "$fixture/contextmanager" \
  "$fixture/artifact" "$fixture/memory" "$fixture/command" "$fixture/provider/openai"
cp "$repo_root/scripts/check-boundaries.sh" "$fixture/scripts/check-boundaries.sh"

for package_dir in message model event permission safespace skill contextmanager artifact memory command provider/openai; do
  package_name=$(basename "$package_dir")
  printf 'package %s\n' "$package_name" > "$fixture/$package_dir/doc.go"
done

mkdir "$fixture/worker"
if (cd "$fixture" && sh scripts/check-boundaries.sh) >/dev/null 2>&1; then
  echo "boundary test accepted a Core worker directory" >&2
  exit 1
fi
rmdir "$fixture/worker"

printf '%s\n' \
  'package message' \
  'import _ "tiggy-manage-agent/internal/runner"' \
  > "$fixture/message/enterprise.go"
if (cd "$fixture" && sh scripts/check-boundaries.sh) >/dev/null 2>&1; then
  echo "boundary test accepted an Enterprise import" >&2
  exit 1
fi

echo "repository boundary checks reject Worker and Enterprise implementation leaks"
