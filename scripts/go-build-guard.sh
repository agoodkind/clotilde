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
  local session_id="${PPID}"
  local marker="$marker_dir/$session_id.done"
  if ( set -o noclobber; : > "$marker" ) 2>/dev/null; then
    (
      cd "$repo_root"
      env GOFLAGS= CLYDE_SKIP_BUILD_GUARD=1 go tool clyde-staticcheck ./...
    )
  fi
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
