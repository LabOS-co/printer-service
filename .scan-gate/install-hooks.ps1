[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

function Test-LabOsScanGateSkip {
    return ($env:CI -eq 'true') `
        -or [bool]$env:JENKINS_URL `
        -or ($env:GITHUB_ACTIONS -eq 'true') `
        -or ($env:LABOS_SCAN_GATE_SKIP -eq '1')
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
try {
    $repoRoot = (& git rev-parse --show-toplevel 2>$null).Trim()
    if (-not $repoRoot) { throw "not a git repo" }
} catch {
    $repoRoot = (Get-Location).Path
}
Set-Location $repoRoot

$ensureTools = Join-Path $repoRoot ".scan-gate/ensure-tools.ps1"
if (-not (Test-Path $ensureTools) -and (Test-Path (Join-Path $scriptDir "ensure-tools.ps1"))) {
    $ensureTools = Join-Path $scriptDir "ensure-tools.ps1"
}

if ((Test-Path $ensureTools) -and -not (Test-LabOsScanGateSkip)) {
    & $ensureTools
} elseif (-not (Test-Path $ensureTools) -and -not (Test-LabOsScanGateSkip)) {
    Write-Host "ensure-tools.ps1 not found; checking PATH only." -ForegroundColor Yellow
}

$missing = $false
if (-not (Get-Command pre-commit -ErrorAction SilentlyContinue)) {
    Write-Host "pre-commit is not installed. Run .scan-gate/repository-bootstrap.ps1 or: py -m pip install --user pre-commit" -ForegroundColor Red
    $missing = $true
}
if (-not (Get-Command trufflehog -ErrorAction SilentlyContinue)) {
    Write-Host "trufflehog is not installed. Run .scan-gate/repository-bootstrap.ps1 or install from GitHub releases." -ForegroundColor Red
    $missing = $true
}
if ($missing) {
    exit 127
}

$usesHusky = $false
if (Test-Path ".husky") {
    $usesHusky = $true
} elseif (Test-Path ".git/hooks/pre-commit") {
    $hookText = Get-Content ".git/hooks/pre-commit" -Raw -ErrorAction SilentlyContinue
    if ($hookText -match 'husky') {
        $usesHusky = $true
    }
}

if ($usesHusky) {
    Write-Host "This repository uses Husky for Git hooks."
    Write-Host "Do not run 'pre-commit install' here - wire scan-gate from .husky/* (for LaaS: pnpm install)."
    exit 0
}

if (-not (Test-Path ".pre-commit-config.yaml") -and (Test-Path ".pre-commit-config.windows.yaml")) {
    Copy-Item ".pre-commit-config.windows.yaml" ".pre-commit-config.yaml"
    Write-Host "Copied .pre-commit-config.windows.yaml to .pre-commit-config.yaml"
}

pre-commit install --hook-type pre-commit --hook-type pre-push
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

pre-commit install-hooks
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

Write-Host "Scan gate installed for pre-commit and pre-push."
