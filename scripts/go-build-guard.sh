#!/usr/bin/env bash
set -euo pipefail

tool="$1"
shift

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
module_file="$repo_root/go.mod"
marker_dir="$repo_root/.tmp/go-build-guard"

codesign_identity() {
  if [[ -n "${CLYDE_CODESIGN_IDENTITY:-}" ]]; then
    printf "%s\n" "$CLYDE_CODESIGN_IDENTITY"
    return 0
  fi

  local config="$repo_root/config.mk"
  if [[ -f "$config" ]]; then
    local configured
    configured=$(
      awk -F '=' '
        /^[[:space:]]*CERT_ID[[:space:]]*[:?+]?=/ {
          value = $2
          sub(/^[[:space:]]+/, "", value)
          sub(/[[:space:]]+$/, "", value)
          print value
          exit
        }
      ' "$config"
    )
    if [[ -n "$configured" && "$configured" != YOUR_CERT_* ]]; then
      printf "%s\n" "$configured"
      return 0
    fi
  fi

  security find-identity -v -p codesigning 2>/dev/null \
    | awk '/Developer ID Application/ { print $2; exit }'
}

link_output_path() {
  local prev=""
  for arg in "$@"; do
    if [[ "$prev" == "-o" ]]; then
      printf "%s\n" "$arg"
      return 0
    fi
    prev="$arg"
  done
}

sign_clyde_executable() {
  if [[ "$(uname)" != "Darwin" ]]; then
    return 0
  fi
  if [[ "${TOOLEXEC_IMPORTPATH:-}" != "goodkind.io/clyde/cmd/clyde" ]]; then
    return 0
  fi

  local output
  output="$(link_output_path "$@")"
  if [[ -z "$output" || ! -f "$output" ]]; then
    printf "clyde build guard: linker output not found for signing\n" >&2
    return 1
  fi
  if ! file -b "$output" | grep -q 'Mach-O'; then
    return 0
  fi

  local identity
  identity="$(codesign_identity)"
  if [[ -z "$identity" ]]; then
    printf "clyde build guard: no Developer ID Application signing identity found\n" >&2
    printf "clyde build guard: set CLYDE_CODESIGN_IDENTITY or CERT_ID in config.mk\n" >&2
    return 1
  fi

  codesign --force \
    --sign "$identity" \
    --identifier "${CLYDE_CODESIGN_IDENTIFIER:-io.goodkind.clyde}" \
    --options runtime \
    --timestamp=none \
    "$output" >&2
  codesign --verify --verbose=2 "$output" >&2
}

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

case "$tool_base" in
  link)
    "$tool" "$@"
    sign_clyde_executable "$@"
    ;;
  *)
    exec "$tool" "$@"
    ;;
esac
