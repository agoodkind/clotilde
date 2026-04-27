#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
guard_flag="-toolexec=$repo_root/scripts/go-build-guard.sh"
current="$(go env GOFLAGS)"

normalize() {
  echo "$1" | xargs
}

remove_guard() {
  local value="$1"
  value="${value//$guard_flag/}"
  normalize "$value"
}

if [[ "${1:-}" == "--uninstall" ]]; then
  updated="$(remove_guard "$current")"
  go env -w GOFLAGS="$updated"
  echo "Removed build guard from GOFLAGS"
  exit 0
fi

if [[ " $current " == *" $guard_flag "* ]]; then
  echo "Build guard already installed"
  exit 0
fi

if [[ -n "$current" ]]; then
  updated="$(normalize "$current $guard_flag")"
else
  updated="$guard_flag"
fi

go env -w GOFLAGS="$updated"
echo "Installed build guard in GOFLAGS"
