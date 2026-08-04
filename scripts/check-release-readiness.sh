#!/bin/sh
set -u

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

expected_module=github.com/uyshero/tiggy-agent-runtime
release_version=
failures=0

usage() {
  echo "usage: $0 [--release vMAJOR.MINOR.PATCH]" >&2
  exit 2
}

pass() {
  echo "PASS: $1"
}

fail() {
  echo "FAIL: $1" >&2
  failures=$((failures + 1))
}

if [ "$#" -gt 0 ]; then
  [ "$#" -eq 2 ] || usage
  [ "$1" = "--release" ] || usage
  release_version=$2
  if ! printf '%s\n' "$release_version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
    echo "release version must be a v-prefixed semantic version, for example v0.1.0" >&2
    exit 2
  fi
fi

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  pass "repository is a Git worktree"
else
  fail "repository is not a Git worktree"
fi

if git rev-parse --verify HEAD >/dev/null 2>&1; then
  pass "Git HEAD exists"
else
  fail "Git HEAD does not exist; create reviewed history before release"
fi

if [ -z "$(git status --porcelain=v1 --untracked-files=all 2>/dev/null)" ]; then
  pass "worktree is clean"
else
  fail "worktree has tracked or untracked changes"
fi

if [ -s LICENSE ]; then
  pass "LICENSE exists and is non-empty"
else
  fail "LICENSE is missing or empty; license selection requires owner approval"
fi

module_path=$(go list -m 2>/dev/null || true)
if [ "$module_path" = "$expected_module" ]; then
  pass "module path is $expected_module"
else
  fail "module path is '$module_path', expected '$expected_module'"
fi

if go mod edit -json 2>/dev/null | grep -Eq '"Replace":[[:space:]]*null[,}]'; then
  pass "go.mod contains no replace directives"
else
  fail "go.mod contains replace directives or could not be inspected"
fi

if [ -f SECURITY.md ] && \
  grep -Fqx '## Supported Versions' SECURITY.md && \
  grep -Fqx '## Reporting a Vulnerability' SECURITY.md && \
  grep -Eq '^Security contact: (mailto:|https://)[^[:space:]]+$' SECURITY.md; then
  pass "security policy declares supported versions and a contact"
else
  fail "SECURITY.md needs Supported Versions, Reporting a Vulnerability, and an explicit monitored contact"
fi

release_heading='^## \[[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$'
if grep -Eq "$release_heading" CHANGELOG.md 2>/dev/null; then
  pass "changelog contains a dated release section"
else
  fail "CHANGELOG.md contains no dated semantic-version release section"
fi

if [ -n "$release_version" ]; then
  changelog_version=${release_version#v}
  if grep -F "## [$changelog_version] - " CHANGELOG.md 2>/dev/null | grep -Eq "$release_heading"; then
    pass "changelog contains release $changelog_version"
  else
    fail "CHANGELOG.md has no dated section for $changelog_version"
  fi

  if git tag --points-at HEAD 2>/dev/null | grep -Fqx "$release_version"; then
    pass "tag $release_version points at HEAD"
  else
    fail "tag $release_version does not point at HEAD"
  fi
else
  echo "INFO: tag check skipped; pass --release vMAJOR.MINOR.PATCH for a release candidate"
fi

if [ "$failures" -ne 0 ]; then
  echo "release readiness audit failed with $failures issue(s)" >&2
  exit 1
fi

echo "release readiness metadata checks passed"
echo "owner approval and public repository availability still require human confirmation"
