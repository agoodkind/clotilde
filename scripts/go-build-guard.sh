#!/usr/bin/env bash
set -euo pipefail

tool="$1"
shift

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
module_file="$repo_root/go.mod"
marker_dir="$repo_root/.tmp/go-build-guard"

run_guard() {
  if [[ "${CLYDE_SKIP_BUILD_GUARD:-}" == "1" ]]; then
    return 0
  fi
  if [[ "${PWD}" != "$repo_root" && "${PWD}" != "$repo_root/"* ]]; then
    return 0
  fi

  mkdir -p "$marker_dir"
  local lock="$marker_dir/run.lock"
  local passed="$marker_dir/passed"
  local mod="$repo_root/go.mod"
  local sum="$repo_root/go.sum"

  # Fast path: a recent run for this go.mod / go.sum state already
  # passed. Skip without taking the lock so concurrent compiles do
  # not pile up.
  if [[ -f "$passed" ]] \
     && [[ "$passed" -nt "$mod" ]] \
     && { [[ ! -f "$sum" ]] || [[ "$passed" -nt "$sum" ]]; }; then
    return 0
  fi

  # Slow path: try to acquire the repo-wide exclusive lock without
  # waiting. Only ONE staticcheck runs at a time across all
  # concurrent go build/test/vet calls. Concurrent compiles that
  # cannot get the lock proceed without re-running staticcheck;
  # the holder writes the passed marker, which makes the fast
  # path above succeed for everyone else.
  exec 9>"$lock" || return 0
  if command -v flock >/dev/null 2>&1; then
    if ! flock -n 9; then
      exec 9>&-
      return 0
    fi
  fi

  # Filter out generated files (.pb.go and anything under /api/)
  # since clyde-staticcheck's upstream analyzers don't uniformly
  # honor the `Code generated ... DO NOT EDIT.` marker. The filter
  # mirrors the standalone `make staticcheck` target so both paths
  # agree on what counts as a finding.
  local out
  out=$(
    cd "$repo_root"
    env GOFLAGS= CLYDE_SKIP_BUILD_GUARD=1 GOMAXPROCS=2 \
      go tool clyde-staticcheck ./... 2>&1
  ) || true
  local filtered
  filtered=$(printf "%s\n" "$out" \
    | grep -Ev "\\.pb\\.go:|/api/" \
    | grep -Ev "^go: error obtaining buildID" \
    || true)
  local rc=0
  if [[ -n "$filtered" ]]; then
    printf "%s\n" "$filtered" >&2
    rc=1
  fi
  if [[ $rc -eq 0 ]]; then
    : > "$passed"
  fi
  if command -v flock >/dev/null 2>&1; then
    flock -u 9 || true
  fi
  exec 9>&-
  return $rc
}

if [[ ! -f "$module_file" ]]; then
  exec "$tool" "$@"
fi

tool_base="$(basename "$tool")"
case "$tool_base" in
  compile|link|asm|cgo)
    run_guard
    ;;
esac

exec "$tool" "$@"
