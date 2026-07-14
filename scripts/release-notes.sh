#!/usr/bin/env bash
# Extract one release's narrative from RELEASES.md.
#
#   scripts/release-notes.sh v0.12.0
#
# Prints the section body (everything under "## v0.12.0 — <date>" up to the next
# "## " heading) to stdout. Exits non-zero if there is no section for the tag —
# which fails the release build on purpose: a release page a stranger cannot read
# is worse than a late release. See .github/RELEASE_NOTES_TEMPLATE.md.
set -euo pipefail

tag="${1:-}"
if [[ -z "$tag" ]]; then
    echo "usage: $0 <tag>   e.g. $0 v0.12.0" >&2
    exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
releases="$repo_root/RELEASES.md"

if [[ ! -f "$releases" ]]; then
    echo "release-notes: $releases not found" >&2
    exit 1
fi

# Match "## v0.12.0" with or without a trailing " — date"; stop at the next "## ".
body="$(awk -v tag="$tag" '
    $0 ~ "^## " tag "([^0-9.]|$)" { found = 1; next }
    found && /^## / { exit }
    found { print }
' "$releases")"

# Strip leading/trailing blank lines.
body="$(printf '%s\n' "$body" | sed -e '/./,$!d' | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')"

if [[ -z "$body" ]]; then
    cat >&2 <<EOF
release-notes: RELEASES.md has no section for $tag.

Add one before tagging — see .github/RELEASE_NOTES_TEMPLATE.md.
The GitHub release page leads with this text; without it the release would
publish as a bare commit log, which is what this check exists to prevent.
EOF
    exit 1
fi

printf '%s\n' "$body"
