#!/usr/bin/env bash
# Extract one release's narrative from RELEASES.md.
#
#   scripts/release-notes.sh v0.12.0
#
# Prints the section body (everything under "## v0.12.0 — <date>" up to the next
# "## " heading) to stdout. Exits non-zero if there is no section for the tag, if
# the heading has no release date, or if the section is still marked a draft —
# each fails the release build on purpose: a release page a stranger cannot read
# is worse than a late release. See .github/RELEASE_NOTES_TEMPLATE.md.
set -euo pipefail

tag="${1:-}"
if [[ -z "$tag" ]]; then
    echo "usage: $0 <tag>   e.g. $0 v0.12.0" >&2
    exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# PGMI_RELEASES_FILE exists so the checks below can be tested against fixtures.
releases="${PGMI_RELEASES_FILE:-$repo_root/RELEASES.md}"

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

# Extract the heading line for the date check.
heading_line="$(awk -v tag="$tag" '
    $0 ~ "^## " tag "([^0-9.]|$)" { print; exit }
' "$releases")"

# Lines between the previous "## " heading (or file start) and the tag heading.
# A DRAFT marker can sit here even with intervening blank lines.
pre_heading="$(awk -v tag="$tag" '
    /^## / && !($0 ~ "^## " tag "([^0-9.]|$)") { buf = ""; next }
    $0 ~ "^## " tag "([^0-9.]|$)" { printf "%s", buf; exit }
    { buf = buf $0 "\n" }
' "$releases")"

# A DRAFT marker anywhere in the pre-heading gap or the section body blocks release.
draft_marker="$(printf '%s\n%s' "$pre_heading" "$body" | grep -iE '<!--[[:space:]]*draft' | head -n 1)" || true

if [[ -n "$draft_marker" ]]; then
    cat >&2 <<EOF
release-notes: the $tag section in RELEASES.md is still marked a draft.

    $draft_marker

Remove that marker once the notes are final. It is here to stop a half-written
narrative reaching the release page.
EOF
    exit 1
fi

if ! printf '%s' "$heading_line" | grep -qE '[0-9]{4}-[0-9]{2}-[0-9]{2}'; then
    cat >&2 <<EOF
release-notes: the $tag heading in RELEASES.md carries no release date.

    $heading_line

Set it to today's date, as in "## $tag — $(date +%Y-%m-%d)". Every other
section is dated; a tag cut with TBD leaves the repo, the docs site and the
source archive describing a release that already exists.
EOF
    exit 1
fi

printf '%s\n' "$body"
