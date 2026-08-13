#!/bin/sh

set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
module_path=github.com/uyshero/tiggy-agent-runtime
apidiff_version=v0.0.0-20260812173653-3d80eb74bc5b
base_version=
release_version=
allow_incompatible=false

usage() {
  echo "usage: $0 [--base vMAJOR.MINOR.PATCH] [--release vMAJOR.MINOR.PATCH] [--allow-incompatible]" >&2
  exit 2
}

valid_stable_version() {
  printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base)
      [ "$#" -ge 2 ] || usage
      base_version=$2
      shift 2
      ;;
    --release)
      [ "$#" -ge 2 ] || usage
      release_version=$2
      shift 2
      ;;
    --allow-incompatible)
      allow_incompatible=true
      shift
      ;;
    *) usage ;;
  esac
done

if [ -n "$base_version" ] && ! valid_stable_version "$base_version"; then
  echo "API base must be a stable v-prefixed semantic version" >&2
  exit 2
fi
if [ -n "$release_version" ] && ! valid_stable_version "$release_version"; then
  echo "release version must be a stable v-prefixed semantic version" >&2
  exit 2
fi

cd "$repository_root"

if [ -z "$base_version" ]; then
  for candidate in $(git tag --merged HEAD --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-version:refname); do
    valid_stable_version "$candidate" || continue
    [ "$candidate" = "$release_version" ] && continue
    base_version=$candidate
    break
  done
fi

if [ -z "$base_version" ]; then
  echo "no stable API baseline tag is reachable from HEAD; pass --base explicitly" >&2
  exit 1
fi
if ! git rev-parse --verify "refs/tags/$base_version^{commit}" >/dev/null 2>&1; then
  echo "API baseline tag does not exist: $base_version" >&2
  exit 1
fi
if ! git merge-base --is-ancestor "$base_version" HEAD; then
  echo "API baseline $base_version is not an ancestor of HEAD" >&2
  exit 1
fi

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/tiggy-api-compat.XXXXXX")
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM
baseline_directory="$temporary_directory/baseline"
mkdir "$baseline_directory"

git archive "$base_version" | tar -x -C "$baseline_directory"

export_api() {
  source_directory=$1
  destination=$2
  (
    cd "$source_directory"
    GOWORK=off go run "golang.org/x/exp/cmd/apidiff@$apidiff_version" \
      -m -w "$destination" "$module_path"
  )
}

export_api "$baseline_directory" "$temporary_directory/baseline.api"
export_api "$repository_root" "$temporary_directory/current.api"

report="$temporary_directory/report.txt"
if GOWORK=off go run "golang.org/x/exp/cmd/apidiff@$apidiff_version" -m \
  "$temporary_directory/baseline.api" "$temporary_directory/current.api" >"$report" 2>&1; then
  cat "$report"
  echo "public Go API is compatible with $base_version"
  exit 0
fi

cat "$report" >&2
if ! grep -Fqx 'Incompatible changes:' "$report"; then
  echo "apidiff failed before producing a compatibility result" >&2
  exit 1
fi
if [ "$allow_incompatible" != true ]; then
  echo "public Go API is incompatible with $base_version" >&2
  echo "pre-v1 exceptions require --allow-incompatible and a Breaking Changes changelog section" >&2
  exit 1
fi

compatibility_version=$release_version
[ -n "$compatibility_version" ] || compatibility_version=$base_version
major=${compatibility_version#v}
major=${major%%.*}
if [ "$major" -ne 0 ]; then
  echo "incompatible public API changes cannot be explicitly allowed at or after v1" >&2
  exit 1
fi

if [ -n "$release_version" ]; then
  changelog_prefix="## [${release_version#v}] - "
else
  changelog_prefix='## Unreleased'
fi
if ! awk -v prefix="$changelog_prefix" '
  index($0, prefix) == 1 { in_section = 1; next }
  in_section && /^## / { exit }
  in_section && $0 == "### Breaking Changes" { found = 1 }
  END { exit !found }
' CHANGELOG.md; then
  echo "explicit pre-v1 API break requires a Breaking Changes section for the candidate" >&2
  exit 1
fi

echo "public Go API incompatibility with $base_version was explicitly acknowledged for pre-v1"
