#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p dist
go build -o dist/clyde-tui-qa ./cmd/clyde-tui-qa
exec dist/clyde-tui-qa --help
