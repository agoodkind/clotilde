#!/usr/bin/env bash
# Runs dist/clyde, rebuilding with "make build" when any *.go in this repo is
# newer than the binary, or the binary is missing. Uses flock(1) on
# .clyde-build.lock to serialize parallel invocations. Requires: flock, make, Go
# (see Makefile). The script chdirs to the repository root, which is the
# directory that contains this script. If make fails, a previous dist/clyde
# (when present and executable) is run instead. Optional v2: re-stale check
# after the lock to avoid waiters all rebuilding; not implemented to keep the
# script small.

script_path=${BASH_SOURCE[0]:-$0}
if [[ $script_path != /* ]]; then
	script_path=$PWD/$script_path
fi
repo_root=$(cd "$(dirname "$script_path")" && pwd) || exit 1
bin_path="$repo_root/dist/clyde"
lock_path="$repo_root/.clyde-build.lock"
cd "$repo_root" || exit 1

need_rebuild=0
if [[ ! -f "$bin_path" ]]; then
	need_rebuild=1
else
	if find "$repo_root" -name '*.go' -newer "$bin_path" 2>/dev/null | read -r; then
		need_rebuild=1
	fi
fi

if ((need_rebuild)); then
	# shellcheck disable=SC2016
	if ! REPO="$repo_root" flock "$lock_path" sh -c 'cd "$REPO" && exec make build'; then
		if [[ -f "$bin_path" && -x "$bin_path" ]]; then
			echo "run.sh: make build failed; running previous $bin_path" >&2
			exec "$bin_path" "$@"
		fi
		echo "run.sh: make build failed and $bin_path is missing or not executable" >&2
		exit 1
	fi
fi
exec "$bin_path" "$@"
