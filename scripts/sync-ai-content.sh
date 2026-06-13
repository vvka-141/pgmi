#!/bin/bash
# sync-ai-content.sh
# Syncs essential skills from .claude/skills/ to internal/ai/content/skills/
# Run this before building to ensure embedded content is up-to-date

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

SOURCE_DIR="$REPO_ROOT/.claude/skills"
TARGET_DIR="$REPO_ROOT/internal/ai/content/skills"

echo "Syncing AI skills from .claude/skills/ to internal/ai/content/skills/"

# Create target directory if needed
mkdir -p "$TARGET_DIR"

# Essential skills to sync
SKILLS=(
    "pgmi-philosophy"
    "pgmi-sql"
    "pgmi-system-design"
    "pgmi-templates"
    "pgmi-testing-review"
    "pgmi-postgres-review"
    "pgmi-security-review"
    "pgmi-api-architecture"
    "pgmi-handler-patterns"
    "pgmi-mcp"
    "pgmi-metadata-system"
    "pgmi-test-architecture"
    "postgresql-patterns"
)

synced=0
for skill in "${SKILLS[@]}"; do
    src="$SOURCE_DIR/$skill/SKILL.md"
    tgt="$TARGET_DIR/$skill.md"

    if [ -f "$src" ]; then
        cp "$src" "$tgt"
        echo "  Synced: $skill"
        synced=$((synced + 1))
    else
        echo "  Skipped: $skill (not found)"
    fi
done

echo ""
echo "Synced $synced skills."
