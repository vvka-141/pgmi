# sync-ai-content.ps1
# Refreshes the local .claude/skills/ mirror FROM the tracked, embedded skills.
# internal/ai/content/skills/ is the source of truth (it ships in the binary);
# .claude/skills/ is gitignored, local-only tooling and must never feed back into it.

$ErrorActionPreference = "Stop"

$scriptDir = $PSScriptRoot
if (-not $scriptDir) {
    $scriptDir = Get-Location
}
$repoRoot = Split-Path -Parent $scriptDir

$sourceDir = Join-Path $repoRoot "internal\ai\content\skills"
$targetDir = Join-Path $repoRoot ".claude\skills"

Write-Host "Refreshing .claude/skills/ from internal/ai/content/skills/ (tracked -> local)"
Write-Host "Source: $sourceDir"
Write-Host "Target: $targetDir"

$synced = 0
foreach ($file in Get-ChildItem -Path $sourceDir -Filter "*.md") {
    $skill = $file.BaseName
    $targetSkillDir = Join-Path $targetDir $skill
    if (-not (Test-Path $targetSkillDir)) {
        New-Item -ItemType Directory -Path $targetSkillDir -Force | Out-Null
    }
    Copy-Item $file.FullName (Join-Path $targetSkillDir "SKILL.md") -Force
    Write-Host "  Synced: $skill"
    $synced++
}

Write-Host "`nSynced $synced skills into .claude/skills/."
