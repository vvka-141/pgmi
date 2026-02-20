#Requires -Version 5.1
<#
.SYNOPSIS
    Installs pgmi on Windows.
.DESCRIPTION
    Downloads the latest pgmi release from GitHub, verifies the SHA256 checksum,
    and installs to $env:LOCALAPPDATA\pgmi (no admin required).
.EXAMPLE
    irm https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.ps1 | iex
.EXAMPLE
    $env:PGMI_VERSION = "v0.9.0"; irm ... | iex
#>

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$Repo = "vvka-141/pgmi"
$InstallDir = if ($env:PGMI_INSTALL_DIR) { $env:PGMI_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "pgmi" }

function Write-Status { param([string]$Msg) Write-Host "[*] $Msg" -ForegroundColor Cyan }
function Write-Ok     { param([string]$Msg) Write-Host "[+] $Msg" -ForegroundColor Green }
function Write-Warn   { param([string]$Msg) Write-Host "[!] $Msg" -ForegroundColor Yellow }
function Write-Err    { param([string]$Msg) Write-Host "[-] $Msg" -ForegroundColor Red }

function Get-Architecture {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        "AMD64" { return "amd64" }
        "ARM64" { return "arm64" }
        default {
            Write-Err "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE"
            exit 1
        }
    }
}

function Get-Version {
    if ($env:PGMI_VERSION) {
        $v = $env:PGMI_VERSION
        if (-not $v.StartsWith("v")) { $v = "v$v" }
        return $v
    }

    Write-Status "Fetching latest release..."
    try {
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing
        return $release.tag_name
    } catch {
        Write-Err "Failed to fetch latest version from GitHub API."
        Write-Err $_.Exception.Message
        exit 1
    }
}

function Install-Pgmi {
    $arch = Get-Architecture
    $tag = Get-Version
    $version = $tag.TrimStart("v")

    Write-Host ""
    Write-Host "=====================================================" -ForegroundColor White
    Write-Host "  pgmi installer for Windows" -ForegroundColor White
    Write-Host "  Version: $tag | Architecture: $arch" -ForegroundColor DarkGray
    Write-Host "=====================================================" -ForegroundColor White
    Write-Host ""

    $fileName = "pgmi_${version}_windows_${arch}.zip"
    $downloadUrl = "https://github.com/$Repo/releases/download/$tag/$fileName"
    $checksumsUrl = "https://github.com/$Repo/releases/download/$tag/checksums.txt"

    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) "pgmi-install-$([System.Guid]::NewGuid().ToString('N').Substring(0,8))"
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

    try {
        Write-Status "Downloading $fileName..."
        $zipPath = Join-Path $tmpDir $fileName
        Invoke-WebRequest -Uri $downloadUrl -OutFile $zipPath -UseBasicParsing
        Write-Ok "Downloaded."

        Write-Status "Downloading checksums..."
        $checksumsPath = Join-Path $tmpDir "checksums.txt"
        Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath -UseBasicParsing

        Write-Status "Verifying SHA256 checksum..."
        $checksumContent = Get-Content $checksumsPath
        $expectedLine = $checksumContent | Where-Object { $_ -match $fileName }
        if (-not $expectedLine) {
            Write-Err "Checksum entry not found for $fileName in checksums.txt"
            exit 1
        }
        $expectedHash = ($expectedLine -split '\s+')[0]
        $sha256 = [System.Security.Cryptography.SHA256]::Create()
        $stream = [System.IO.File]::OpenRead($zipPath)
        try {
            $hashBytes = $sha256.ComputeHash($stream)
            $actualHash = [BitConverter]::ToString($hashBytes).Replace("-", "").ToLower()
        } finally {
            $stream.Close()
            $sha256.Dispose()
        }

        if ($actualHash -ne $expectedHash) {
            Write-Err "Checksum mismatch!"
            Write-Err "  Expected: $expectedHash"
            Write-Err "  Actual:   $actualHash"
            exit 1
        }
        Write-Ok "Checksum verified."

        Write-Status "Extracting to $InstallDir..."
        if (-not (Test-Path $InstallDir)) {
            New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        }

        $extractDir = Join-Path $tmpDir "extract"
        Expand-Archive -Path $zipPath -DestinationPath $extractDir -Force
        Copy-Item -Path (Join-Path $extractDir "pgmi.exe") -Destination (Join-Path $InstallDir "pgmi.exe") -Force
        Write-Ok "Installed pgmi.exe to $InstallDir"

        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        if ($userPath -notlike "*$InstallDir*") {
            Write-Status "Adding $InstallDir to user PATH..."
            [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
            $env:Path = "$InstallDir;$env:Path"
            Write-Ok "PATH updated."
        } else {
            Write-Ok "$InstallDir already in PATH."
        }

        Write-Host ""
        $pgmiExe = Join-Path $InstallDir "pgmi.exe"
        if (Test-Path $pgmiExe) {
            $v = & $pgmiExe --version 2>&1
            Write-Host "=====================================================" -ForegroundColor Green
            Write-Host "  pgmi installed successfully!" -ForegroundColor Green
            Write-Host "  $v" -ForegroundColor Green
            Write-Host "=====================================================" -ForegroundColor Green
        } else {
            Write-Warn "Installation completed but pgmi.exe not found at $pgmiExe"
        }
        Write-Host ""
    } finally {
        if (Test-Path $tmpDir) {
            Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

Install-Pgmi
