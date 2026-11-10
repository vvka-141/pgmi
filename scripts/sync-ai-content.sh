#!/bin/bash
# sync-ai-content.sh
# Refreshes the local .claude/skills/ mirror FROM the skills pgmi scaffolds:
# shared/skills/ (every template) and
# shared/templates/<name>/.agents/skills/ (one template's own).
# Those tracked trees are the source of truth; .claude/skills/ is gitignored,
# local-only tooling and must never feed back into them.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
TARGET_DIR="$REPO_ROOT/.claude/skills"

echo "Refreshing .claude/skills/ from the scaffold skill trees (tracked -> local)"

synced=0
for src in "$REPO_ROOT"/shared/skills/*/ "$REPO_ROOT"/shared/templates/*/.agents/skills/*/; do
    [ -f "$src/SKILL.md" ] || continue
    skill="$(basename "$src")"
    rm -rf "${TARGET_DIR:?}/$skill"
    mkdir -p "$TARGET_DIR/$skill"
    cp -R "$src". "$TARGET_DIR/$skill/"
    echo "  Synced: $skill"
    synced=$((synced + 1))
done

echo ""
echo "Synced $synced skills into .claude/skills/."
