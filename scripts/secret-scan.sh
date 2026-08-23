#!/bin/bash
# Scan the working tree and, with --history, every commit not yet on the public
# main, for credential shapes that must never reach this repo. hostit is public;
# the secrets live in the private ansible repo and belong only there.
#
# Placeholders that merely document a FORMAT ("sk-ant-...", "GOCSPX-...") are
# fine and are excluded by requiring enough trailing characters to be real. The
# private-key marker is anchored to a whole line for the same reason: a pasted
# key has it alone on a line, while a placeholder sits inside a quoted string.
set -euo pipefail

# Each pattern is a credential shape with enough length to distinguish a real
# value from a documented prefix.
PATTERNS=(
  'GOCSPX-[A-Za-z0-9_-]{20,}'          # Google OAuth client secret
  'sk-ant-[A-Za-z0-9_-]{40,}'          # Anthropic key or OAuth token
  'github_pat_[A-Za-z0-9_]{40,}'       # GitHub fine-grained PAT
  'gh[pousr]_[A-Za-z0-9]{30,}'         # GitHub classic token
  'AKIA[0-9A-Z]{16}'                   # AWS access key id
  'xox[baprs]-[A-Za-z0-9-]{20,}'       # Slack token
  '^-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----$'  # a real key: the marker on its own line
  '[0-9]{17,20}\.[A-Za-z0-9_-]{27}\.'  # Discord bot token
)

scan() { # scan <label> <reader-command...>
  local label="$1"; shift
  local hits=0
  for p in "${PATTERNS[@]}"; do
    if "$@" | grep -InE "$p" >/dev/null 2>&1; then
      echo "FOUND in $label: $p"
      "$@" | grep -InE "$p" | head -5
      hits=1
    fi
  done
  return $hits
}

status=0
tree() { git grep -InE "$1" -- . ':!scripts/secret-scan.sh' 2>/dev/null || true; }
for p in "${PATTERNS[@]}"; do
  out=$(tree "$p")
  if [ -n "$out" ]; then echo "== working tree =="; echo "$out" | head -5; status=1; fi
done

if [ "${1:-}" = "--history" ]; then
  base="${2:-origin/main}"
  for p in "${PATTERNS[@]}"; do
    out=$(git diff "$base"...HEAD -- . ':!scripts/secret-scan.sh' | grep -InE "^\+.*$p" || true)
    if [ -n "$out" ]; then echo "== commits since $base =="; echo "$out" | head -5; status=1; fi
  done
fi

if [ "$status" -eq 0 ]; then echo "clean: no credential shapes found"; fi
exit "$status"
