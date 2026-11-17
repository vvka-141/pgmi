#!/usr/bin/env bash
# Usage: placeholders.sh <file>
# Fails when <file> still holds a licence template's copyright placeholder:
# MIT's "<year> <copyright holders>", BSD's and ISC's "<year> <owner>".
set -euo pipefail
file="${1:?file}"
if grep -nEi '^[[:space:]]*copyright.*<(year|owner|copyright holders?)>' "$file" >&2; then
  echo "$file holds a licence template placeholder (lines above): add a clarify entry to packaging/about.toml" >&2
  exit 1
fi
