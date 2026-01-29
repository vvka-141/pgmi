#Requires -Version 5.1
<#
.SYNOPSIS
    Sets up a Windows development environment for pgmi with dockerized PostgreSQL.
.DESCRIPTION
    Checks and installs: Git, Go, container runtime (Docker Desktop or Rancher Desktop),
    then pulls the Azure Flex PostgreSQL 17 image and starts a ready-to-use container.
#>

# ============================================================================
# CONFIGURATION — Edit these values to match your preferences
# ============================================================================

$ContainerRuntime  = "DockerDesktop"   # "DockerDesktop" or "RancherDesktop"
$ContainerName     = "pgmi-postgres17-azflex"
$PostgresImage     = "alexeye/postgres-azure-flex:17"
$PostgresPort      = 5432
$PostgresUser      = "postgres"
$PostgresPassword  = "postgres"
$DataVolumePath    = "$env:USERPROFILE\.pgmi\pgdata17"
$PgmiRepo          = "github.com/vvka-141/pgmi/cmd/pgmi@latest"

# ============================================================================
# INTERNALS
# ============================================================================

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Status  { param([string]$Msg) Write-Host "[*] $Msg" -ForegroundColor Cyan }
function Write-Ok      { param([string]$Msg) Write-Host "[+] $Msg" -ForegroundColor Green }
function Write-Warn    { param([string]$Msg) Write-Host "[!] $Msg" -ForegroundColor Yellow }
function Write-Err     { param([string]$Msg) Write-Host "[-] $Msg" -ForegroundColor Red }

function Test-Command { param([string]$Name) $null -ne (Get-Command $Name -ErrorAction SilentlyContinue) }

function Invoke-Native {
    param([string]$Command, [string[]]$Args)
    $prevPref = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $output = & $Command @Args 2>&1 | Out-String
    $code = $LASTEXITCODE
    $ErrorActionPreference = $prevPref
    return [PSCustomObject]@{ Output = $output.Trim(); ExitCode = $code }
}

function Test-WingetAvailable {
    try { winget --version | Out-Null; return $true } catch { return $false }
}

function Install-ViaWinget {
    param([string]$PackageId, [string]$DisplayName)
    if (-not (Test-WingetAvailable)) {
        Write-Err "winget is not available. Please install $DisplayName manually."
        return $false
    }
    Write-Status "Installing $DisplayName via winget..."
    winget install --id $PackageId --exact --accept-source-agreements --accept-package-agreements
    if ($LASTEXITCODE -ne 0) {
        Write-Err "Failed to install $DisplayName via winget."
        return $false
    }
    Write-Ok "$DisplayName installed. You may need to restart this terminal for PATH changes."
    return $true
}

function Refresh-Path {
    $machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
    $userPath    = [Environment]::GetEnvironmentVariable("Path", "User")
    $env:Path    = "$machinePath;$userPath"
}

# ============================================================================
# PRE-FLIGHT: WSL2
# ============================================================================

function Assert-Wsl2 {
    Write-Status "Checking WSL2 availability..."

    if (Test-Command "wsl") {
        $r = Invoke-Native "wsl" "--status"
        if ($r.ExitCode -eq 0 -or $r.Output -match "WSL|Default") {
            Write-Ok "WSL2 is available."
            return
        }
    }

    Write-Err "WSL2 is not installed or not functional. Both Docker Desktop and Rancher Desktop require WSL2."
    Write-Warn "Please run the following in an elevated (Admin) PowerShell, then reboot:"
    Write-Host ""
    Write-Host "  wsl --install" -ForegroundColor White
    Write-Host ""
    Write-Warn "After reboot, re-run this script."
    exit 1
}

# ============================================================================
# STEP 1: GIT
# ============================================================================

function Assert-Git {
    Write-Status "Checking Git..."
    Refresh-Path
    if (Test-Command "git") {
        $v = git --version 2>&1
        Write-Ok "Git found: $v"
        return
    }
    Write-Warn "Git not found."
    if (Install-ViaWinget "Git.Git" "Git") {
        Refresh-Path
        if (Test-Command "git") { Write-Ok "Git is now available."; return }
        Write-Warn "Git installed but not on PATH yet. Please restart your terminal and re-run."
        exit 1
    }
    Write-Err "Please install Git manually: https://git-scm.com/download/win"
    exit 1
}

# ============================================================================
# STEP 2: GO
# ============================================================================

function Assert-Go {
    Write-Status "Checking Go..."
    Refresh-Path
    if (Test-Command "go") {
        $v = go version 2>&1
        Write-Ok "Go found: $v"
        return
    }
    Write-Warn "Go not found."
    if (Install-ViaWinget "GoLang.Go" "Go") {
        Refresh-Path
        if (Test-Command "go") { Write-Ok "Go is now available."; return }
        Write-Warn "Go installed but not on PATH yet. Please restart your terminal and re-run."
        exit 1
    }
    Write-Err "Please install Go manually: https://go.dev/dl/"
    exit 1
}

# ============================================================================
# STEP 3: CONTAINER RUNTIME
# ============================================================================

function Assert-ContainerRuntime {
    Write-Status "Checking container runtime ($ContainerRuntime)..."
    Refresh-Path

    if (Test-Command "docker") {
        $r = Invoke-Native "docker" "info"
        if ($r.ExitCode -eq 0) {
            Write-Ok "Docker CLI is functional."
            return
        }
    }

    Write-Warn "Docker CLI not functional."

    switch ($ContainerRuntime) {
        "DockerDesktop" {
            $installed = Get-ItemProperty "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*",
                                          "HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*" -ErrorAction SilentlyContinue |
                         Where-Object { $_.DisplayName -like "*Docker Desktop*" }
            if ($installed) {
                Write-Warn "Docker Desktop is installed but the engine is not running."
                Write-Warn "Please start Docker Desktop, wait for it to initialize, then re-run this script."
                exit 1
            }
            Write-Warn "Docker Desktop not found. Attempting install via winget..."
            if (Install-ViaWinget "Docker.DockerDesktop" "Docker Desktop") {
                Write-Warn "Docker Desktop installed. Please launch it, complete initial setup, then re-run this script."
                exit 1
            }
            Write-Err "Please install Docker Desktop manually: https://www.docker.com/products/docker-desktop/"
            exit 1
        }
        "RancherDesktop" {
            $installed = Get-ItemProperty "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*",
                                          "HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*" -ErrorAction SilentlyContinue |
                         Where-Object { $_.DisplayName -like "*Rancher Desktop*" }
            if ($installed) {
                Write-Warn "Rancher Desktop is installed but the engine is not running."
                Write-Warn "Please start Rancher Desktop (with dockerd/moby engine), wait for it to initialize, then re-run this script."
                exit 1
            }
            Write-Warn "Rancher Desktop not found. Attempting install via winget..."
            if (Install-ViaWinget "suse.RancherDesktop" "Rancher Desktop") {
                Write-Warn "Rancher Desktop installed. Please launch it, select 'dockerd (moby)' as engine, then re-run this script."
                exit 1
            }
            Write-Err "Please install Rancher Desktop manually: https://rancherdesktop.io/"
            exit 1
        }
        default {
            Write-Err "Unknown ContainerRuntime: '$ContainerRuntime'. Use 'DockerDesktop' or 'RancherDesktop'."
            exit 1
        }
    }
}

# ============================================================================
# STEP 4: PGMI
# ============================================================================

function Assert-Pgmi {
    Write-Status "Checking pgmi..."
    Refresh-Path

    $gopath = go env GOPATH 2>&1
    $gopathBin = Join-Path $gopath "bin"
    if ($env:Path -notlike "*$gopathBin*") {
        $env:Path = "$gopathBin;$env:Path"
    }

    if (Test-Command "pgmi") {
        $v = pgmi --version 2>&1
        Write-Ok "pgmi found: $v"
        return
    }

    Write-Status "Installing pgmi via go install..."
    $r = Invoke-Native "go" @("install", $PgmiRepo)
    if ($r.ExitCode -ne 0) {
        Write-Err "Failed to install pgmi. Check Go and Git are working, then retry."
        exit 1
    }
    Refresh-Path
    if ($env:Path -notlike "*$gopathBin*") {
        $env:Path = "$gopathBin;$env:Path"
    }

    if (Test-Command "pgmi") {
        $v = pgmi --version 2>&1
        Write-Ok "pgmi installed: $v"
    } else {
        Write-Warn "pgmi installed but not found on PATH."
        Write-Warn "Add this to your PATH: $gopathBin"
        exit 1
    }
}

# ============================================================================
# STEP 5: POSTGRESQL CONTAINER
# ============================================================================

function Assert-PostgresContainer {
    Write-Status "Setting up PostgreSQL container ($ContainerName)..."

    if (-not (Test-Path $DataVolumePath)) {
        Write-Status "Creating data directory: $DataVolumePath"
        New-Item -ItemType Directory -Path $DataVolumePath -Force | Out-Null
    }

    $r = Invoke-Native "docker" @("ps", "-a", "--filter", "name=^${ContainerName}$", "--format", "{{.Status}}")
    if ($r.Output) {
        if ($r.Output -like "Up*") {
            Write-Ok "Container '$ContainerName' is already running."
        } else {
            Write-Status "Starting existing container '$ContainerName'..."
            $null = Invoke-Native "docker" @("start", $ContainerName)
            Write-Ok "Container '$ContainerName' started."
        }
    } else {
        Write-Status "Pulling image $PostgresImage..."
        $r = Invoke-Native "docker" @("pull", $PostgresImage)
        if ($r.ExitCode -ne 0) {
            Write-Err "Failed to pull image. Check your internet connection and Docker login."
            exit 1
        }

        Write-Status "Creating container '$ContainerName'..."
        $r = Invoke-Native "docker" @(
            "run", "-d",
            "--name", $ContainerName,
            "-e", "POSTGRES_USER=$PostgresUser",
            "-e", "POSTGRES_PASSWORD=$PostgresPassword",
            "-p", "${PostgresPort}:5432",
            "-v", "${DataVolumePath}:/var/lib/postgresql/data",
            "--restart", "unless-stopped",
            $PostgresImage
        )
        if ($r.ExitCode -ne 0) {
            Write-Err "Failed to create container."
            exit 1
        }
        Write-Ok "Container '$ContainerName' created."
    }

    Write-Status "Waiting for PostgreSQL to accept connections..."
    $maxAttempts = 30
    for ($i = 1; $i -le $maxAttempts; $i++) {
        $r = Invoke-Native "docker" @("exec", $ContainerName, "pg_isready", "-U", $PostgresUser)
        if ($r.ExitCode -eq 0) {
            Write-Ok "PostgreSQL is ready."
            return
        }
        Start-Sleep -Seconds 1
    }
    Write-Err "PostgreSQL did not become ready in time. Check: docker logs $ContainerName"
    exit 1
}

# ============================================================================
# MAIN
# ============================================================================

Write-Host ""
Write-Host "=====================================================" -ForegroundColor White
Write-Host "  pgmi Development Environment Setup" -ForegroundColor White
Write-Host "  Runtime: $ContainerRuntime | Image: $PostgresImage" -ForegroundColor DarkGray
Write-Host "=====================================================" -ForegroundColor White
Write-Host ""

Assert-Wsl2
Assert-Git
Assert-Go
Assert-ContainerRuntime
Assert-Pgmi
Assert-PostgresContainer

Write-Host ""
Write-Host "=====================================================" -ForegroundColor Green
Write-Host "  Setup complete!" -ForegroundColor Green
Write-Host "=====================================================" -ForegroundColor Green
Write-Host ""
Write-Host "  Connection string:" -ForegroundColor White
Write-Host "  postgresql://${PostgresUser}:${PostgresPassword}@localhost:${PostgresPort}/postgres" -ForegroundColor Yellow
Write-Host ""
Write-Host "  Example pgmi command:" -ForegroundColor White
Write-Host "  pgmi deploy ./myapp -d mydb --connection `"postgresql://${PostgresUser}:${PostgresPassword}@localhost:${PostgresPort}/postgres`"" -ForegroundColor Yellow
Write-Host ""
