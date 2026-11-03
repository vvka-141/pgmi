# sync-ai-content.ps1
# Refreshes the local .claude/skills/ mirror FROM the skills pgmi scaffolds:
# internal/scaffold/skills/ (every template) and
# internal/scaffold/templates/<name>/.agents/skills/ (one template's own).
# Those tracked trees are the source of truth; .claude/skills/ is gitignored,
# local-only tooling and must never feed back into them.

$ErrorActionPreference = "Stop"

$scriptDir = $PSScriptRoot
if (-not $scriptDir) {
    $scriptDir = Get-Location
}
$repoRoot = Split-Path -Parent $scriptDir
$targetDir = Join-Path $repoRoot ".claude\skills"

Write-Host "Refreshing .claude/skills/ from the scaffold skill trees (tracked -> local)"

$sources = @(Get-ChildItem -Directory (Join-Path $repoRoot "internal\scaffold\skills"))
foreach ($template in Get-ChildItem -Directory (Join-Path $repoRoot "internal\scaffold\templates")) {
    $templateSkills = Join-Path $template.FullName ".agents\skills"
    if (Test-Path $templateSkills) {
        $sources += Get-ChildItem -Directory $templateSkills
    }
}

$synced = 0
foreach ($skill in $sources) {
    if (-not (Test-Path (Join-Path $skill.FullName "SKILL.md"))) { continue }
    $targetSkillDir = Join-Path $targetDir $skill.Name
    if (Test-Path $targetSkillDir) {
        Remove-Item -Recurse -Force $targetSkillDir
    }
    Copy-Item -Recurse $skill.FullName $targetSkillDir
    Write-Host "  Synced: $($skill.Name)"
    $synced++
}

Write-Host "`nSynced $synced skills into .claude/skills/."
