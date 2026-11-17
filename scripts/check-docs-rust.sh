#!/usr/bin/env bash
# Fails when public docs still describe the Go implementation of pgmi:
# installing or building pgmi with Go, the Go badge, or pgmi's Go packages.
# Go stays legitimate for the advanced template's Go gateway and clients and
# for the go-legacy tag; scripts/check-docs-rust.allow lists the rest.
set -euo pipefail
cd "$(dirname "$0")/.."

pattern='go install [^ ]*vvka-141/pgmi|go (build|run|test|vet) [^ ]*\./(cmd|internal|pkg)\b|go test \./\.\.\.|github\.com/vvka-141/pgmi/(pkg|internal|cmd)\b|\bpkg/pgmi\b|GO-LIBRARY|img\.shields\.io/github/go-mod|badge/Go-|\bGo [Vv]ersion\b|written in Go|\bthe Go (binary|core|code|preprocessor|scanner|suite|tests?)\b|\bGo preprocessor\b|\bby Go\b|\binternal/(scaffold|contract|services|db|mcp|cli|params|files|preprocessor)/|\bcmd/pgmi\b|\bjackc/pgx\b|\bpgx\b'
allow=scripts/check-docs-rust.allow

files=$(git ls-files README.md CONTRIBUTING.md SUPPORT.md SECURITY.md docs website/content website/static/llms.txt shared/ai \
    | grep -E '\.(md|txt)$' | grep -vxF -f <(grep -v '^#' "$allow" | grep -v ':' | sed '/^$/d') || true)

status=0
while IFS= read -r hit; do
    [ -z "$hit" ] && continue
    file=${hit%%:*}
    rest=${hit#*:}
    text=${rest#*:}
    case "$text" in *go-legacy*|*gateway*|*Gateway*) continue ;; esac
    skip=0
    while IFS= read -r rule; do
        case "$rule" in ''|'#'*) continue ;; esac
        [[ "$rule" == *:* ]] || continue
        if [[ "$file" == "${rule%%:*}" && "$text" == *"${rule#*:}"* ]]; then skip=1; break; fi
    done < "$allow"
    [ "$skip" = 1 ] && continue
    echo "check-docs-rust: $hit" >&2
    status=1
done < <(printf '%s\n' "$files" | xargs -r grep -snEH "$pattern" || true)

if [ "$status" != 0 ]; then
    echo "check-docs-rust: public docs above still describe the Go implementation; rewrite them for the Rust binary, or add a justified entry to $allow" >&2
fi
exit "$status"
