<#
.SYNOPSIS
    The pre-release gate, for machines without make.

.DESCRIPTION
    Runs the same checks as `make release-ready`, in the same order. make is
    not installed on a default Windows box — not in PowerShell and not in Git
    Bash — so the documented command cannot run there at all, and the gate
    would otherwise be skipped by whoever is most likely to need it.

    Keep this in step with the release-ready target in the Makefile.
    TestReleaseReadyParityWithMakefile fails when the two drift apart.

.PARAMETER Tag
    The tag being prepared, e.g. v0.12.0. Checked against RELEASES.md.

.EXAMPLE
    ./scripts/release-ready.ps1 -Tag v0.12.0
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^v\d+\.\d+\.\d+')]
    [string]$Tag
)

$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)

$failures = @()

function Invoke-Gate {
    param([string]$Name, [scriptblock]$Body)

    Write-Host ""
    Write-Host "=== $Name ===" -ForegroundColor Cyan
    & $Body
    if ($LASTEXITCODE -ne 0) {
        $script:failures += $Name
        Write-Host "$Name : FAIL (exit $LASTEXITCODE)" -ForegroundColor Red
    } else {
        Write-Host "$Name : OK" -ForegroundColor Green
    }
}

Invoke-Gate "lint (native)" { golangci-lint run }
Invoke-Gate "lint (GOOS=linux)" {
    $prev = $env:GOOS
    $env:GOOS = 'linux'
    try { golangci-lint run } finally { $env:GOOS = $prev }
}

# The full suite, not -short: the integration tests are the ones that would
# catch a template or session-API regression before it reaches a user.
Invoke-Gate "full suite" { go test ./... -count=1 -timeout 40m }
Invoke-Gate "connection & security tests" {
    go test -tags conntest -timeout 5m ./internal/db/conntest/...
}

# bash, not PowerShell: release-notes.sh is what the tag workflow runs, so
# running the same script is the only check that means anything.
Invoke-Gate "release notes for $Tag" {
    bash ./scripts/release-notes.sh $Tag | Out-Null
}

Invoke-Gate "govulncheck" {
    go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
}

if (Get-Command goreleaser -ErrorAction SilentlyContinue) {
    Invoke-Gate "goreleaser check" { goreleaser check }
} else {
    Write-Host ""
    Write-Host "goreleaser check: SKIPPED (not installed; CI runs it on the tag)" -ForegroundColor Yellow
}

Invoke-Gate "build" { go build -o pgmi.exe ./cmd/pgmi }

Write-Host ""
Write-Host "Not covered here — the tag workflow runs these:"
Write-Host "  * the five end-to-end example gates (.github/workflows/examples.yml)"
Write-Host "  * the full snapshot build — archives, .deb, checksums for all 6 targets"
Write-Host "    (.github/workflows/snapshot.yml; goreleaser check here only reads the config)"
Write-Host "  * the race detector (needs CGO)"
Write-Host "  * the tag-is-on-main provenance check"
Write-Host ""
Write-Host "Dispatch the snapshot build before tagging:" -ForegroundColor Yellow
Write-Host "  gh workflow run snapshot.yml --ref main"
Write-Host "It is the only gate that renders goreleaser templates, and it runs"
Write-Host "nowhere else until the tag build — where a failure arrives too late."

if ($failures.Count -gt 0) {
    Write-Host ""
    Write-Host "release-ready: FAILED — $($failures -join ', ')" -ForegroundColor Red
    exit 1
}

Write-Host ""
Write-Host "release-ready: all gates passed for $Tag" -ForegroundColor Green
