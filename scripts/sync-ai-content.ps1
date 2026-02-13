# sync-ai-content.ps1
# Syncs skills from .claude/skills/ to internal/ai/content/skills/
# Run this before building to ensure embedded content is up-to-date

$ErrorActionPreference = "Stop"

# Get repo root - script is in scripts/ subdirectory
$scriptDir = $PSScriptRoot
if (-not $scriptDir) {
    $scriptDir = Get-Location
}
$repoRoot = Split-Path -Parent $scriptDir

$sourceDir = Join-Path $repoRoot ".claude\skills"
$targetDir = Join-Path $repoRoot "internal\ai\content\skills"

Write-Host "Syncing AI skills from .claude/skills/ to internal/ai/content/skills/"
Write-Host "Source: $sourceDir"
Write-Host "Target: $targetDir"

# Create target directory if it doesn't exist
if (-not (Test-Path $targetDir)) {
    New-Item -ItemType Directory -Path $targetDir -Force | Out-Null
}

# List of essential skills to sync (add more as needed)
$essentialSkills = @(
    "pgmi-philosophy",
    "pgmi-sql",
    "pgmi-cli",
    "pgmi-templates",
    "pgmi-testing-review",
    "pgmi-postgres-review",
    "pgmi-api-architecture",
    "pgmi-mcp",
    "pgmi-connections",
    "pgmi-deployment"
)

$synced = 0
foreach ($skill in $essentialSkills) {
    $sourcePath = Join-Path $sourceDir "$skill\SKILL.md"
    $targetPath = Join-Path $targetDir "$skill.md"

    if (Test-Path $sourcePath) {
        Copy-Item $sourcePath $targetPath -Force
        Write-Host "  Synced: $skill"
        $synced++
    } else {
        Write-Host "  Skipped: $skill (not found)"
    }
}

Write-Host "`nSynced $synced skills."
Write-Host "Run 'go build ./...' to embed updated content."
