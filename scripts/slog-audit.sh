#!/usr/bin/env bash
# slog-audit: fail when any production .go file uses a banned LOGGING
# pattern (see docs/SLOG.md). User-facing CLI output via Fprint* to a
# writer (cmd.OutOrStdout, cmd.ErrOrStderr, etc.) is allowed; bare
# fmt.Print / fmt.Println / fmt.Printf are banned because they always
# go to stdout and bypass the JSONL trace.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Banned patterns:
#   1. log.Print* / log.Fatal* / log.Panic*            --  stdlib log goes nowhere structured.
#   2. slog.Info / slog.Debug / slog.Warn / slog.Error  --  direct calls bypass slogger wrapper.
#   3. fmt.Print / fmt.Println / fmt.Printf             --  bare stdout writes; use slogger.Event or cmd.OutOrStdout.
BANNED_RE='(\blog\.(Print|Println|Printf|Fatal[a-z]*|Panic[a-z]*)\b|\bfmt\.(Print|Println|Printf)\b\()'

EXEMPT_REGEX='(^|/)(_test\.go|scripts/|research/|vendor/|node_modules/|\.git/|internal/slogger/slogger\.go|cmd/version\.go|cmd/completion\.go)'

mapfile -t HITS < <(grep -RnE "$BANNED_RE" --include='*.go' . | grep -vE "$EXEMPT_REGEX")

if [[ ${#HITS[@]} -eq 0 ]]; then
  echo "slog-audit: clean."
  exit 0
fi

echo "slog-audit: ${#HITS[@]} banned logging call sites remain."
echo
echo "Per-package counts:"
printf '%s\n' "${HITS[@]}" | awk -F: '{print $1}' | xargs -n1 dirname | sort | uniq -c | sort -rn | head -30
echo
echo "First 30 offending call sites:"
printf '%s\n' "${HITS[@]}" | head -30
exit 1
