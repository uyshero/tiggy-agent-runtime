#!/bin/sh

set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
manifest="$repository_root/licenses/go-modules.txt"
temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/tiggy-license-check.XXXXXX")
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM

approved_modules="$temporary_directory/approved-modules.txt"
resolved_modules="$temporary_directory/resolved-modules.txt"
selected_modules="$temporary_directory/selected-modules.txt"

if [ ! -f "$manifest" ]; then
  echo "dependency-license manifest is missing: $manifest" >&2
  exit 1
fi

if ! awk -F '|' '
  /^[[:space:]]*($|#)/ { next }
  NF != 3 || $1 == "" || $2 == "" || $3 == "" {
    printf "invalid dependency-license entry at line %d\n", NR > "/dev/stderr"
    invalid = 1
    next
  }
  $3 != "Apache-2.0" &&
  $3 != "MIT" &&
  $3 != "BSD-2-Clause" &&
  $3 != "BSD-3-Clause" &&
  $3 != "ISC" &&
  $3 != "Apache-2.0 AND MIT AND CC-BY-4.0" &&
  $3 != "MIT AND Apache-2.0" {
    printf "unapproved SPDX expression at line %d: %s\n", NR, $3 > "/dev/stderr"
    invalid = 1
    next
  }
  { print $1 "|" $2 }
  END { exit invalid }
' "$manifest" > "$approved_modules"; then
  exit 1
fi

if ! LC_ALL=C sort -c "$approved_modules"; then
  echo "dependency-license manifest must be sorted by module and version" >&2
  exit 1
fi

duplicate_modules=$(LC_ALL=C sort "$approved_modules" | uniq -d)
if [ -n "$duplicate_modules" ]; then
  echo "duplicate dependency-license entries:" >&2
  echo "$duplicate_modules" >&2
  exit 1
fi

if ! (
  cd "$repository_root"
  GOWORK=off go list -mod=readonly -m \
    -f '{{if not .Main}}{{.Path}}|{{.Version}}{{end}}' all > "$resolved_modules"
); then
  echo "cannot resolve the Go module build list" >&2
  exit 1
fi
sed '/^$/d' "$resolved_modules" | LC_ALL=C sort > "$selected_modules"

unreviewed_modules=$(comm -13 "$approved_modules" "$selected_modules")
stale_modules=$(comm -23 "$approved_modules" "$selected_modules")

if [ -n "$unreviewed_modules" ] || [ -n "$stale_modules" ]; then
  if [ -n "$unreviewed_modules" ]; then
    echo "unreviewed modules in the Go build list:" >&2
    echo "$unreviewed_modules" >&2
  fi
  if [ -n "$stale_modules" ]; then
    echo "stale modules in the dependency-license manifest:" >&2
    echo "$stale_modules" >&2
  fi
  echo "review every dependency change and update licenses/go-modules.txt" >&2
  exit 1
fi

module_count=$(wc -l < "$selected_modules" | tr -d ' ')
echo "dependency licenses are approved for $module_count modules"
