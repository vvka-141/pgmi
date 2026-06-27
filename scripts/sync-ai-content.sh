#!/bin/bash
# sync-ai-content.sh
# Refreshes the local .claude/skills/ mirror FROM the tracked, embedded skills.
# internal/ai/content/skills/ is the source of truth (it ships in the binary);
# .claude/skills/ is gitignored, local-only tooling and must never feed back into it.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

SOURCE_DIR="$REPO_ROOT/internal/ai/content/skills"
TARGET_DIR="$REPO_ROOT/.claude/skills"

echo "Refreshing .claude/skills/ from internal/ai/content/skills/ (tracked -> local)"

synced=0
for src in "$SOURCE_DIR"/*.md; do
    [ -f "$src" ] || continue
    skill="$(basename "$src" .md)"
    mkdir -p "$TARGET_DIR/$skill"
    cp "$src" "$TARGET_DIR/$skill/SKILL.md"
    echo "  Synced: $skill"
    synced=$((synced + 1))
done

echo ""
echo "Synced $synced skills into .claude/skills/."
