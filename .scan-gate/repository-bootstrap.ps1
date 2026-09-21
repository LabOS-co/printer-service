[CmdletBinding()]
param(
    [switch]$ExistingClone,
    [switch]$SkipHooks
)

$ErrorActionPreference = "Stop"

function Get-LabOsDefaultGitTemplate {
    $execPath = (& git --exec-path 2>$null)
    if ($execPath) {
        $execPath = $execPath.Trim()
        $candidate = Join-Path (Split-Path (Split-Path $execPath -Parent) -Parent) "share\git-core\templates"
        if (Test-Path (Join-Path $candidate "hooks")) {
            return $candidate
        }
        $mingw = Join-Path (Split-Path $execPath -Parent) "..\share\git-core\templates"
        $resolved = [System.IO.Path]::GetFullPath($mingw)
        if (Test-Path (Join-Path $resolved "hooks")) {
            return $resolved
        }
    }
    $pf = ${env:ProgramFiles}
    $guesses = @()
    if ($pf) {
        $guesses += (Join-Path $pf "Git\mingw64\share\git-core\templates")
    }
    foreach ($d in $guesses) {
        if ($d -and (Test-Path (Join-Path $d "hooks"))) {
            return $d
        }
    }
    return $null
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$templateSrc = Join-Path $scriptDir "git-template\hooks\post-checkout"
$templateDest = Join-Path $env:USERPROFILE ".labos-git-template"

& (Join-Path $scriptDir "ensure-tools.ps1")

if (-not (Test-Path $templateSrc)) {
    Write-Error "git template hook not found at $templateSrc"
    exit 1
}

if (Test-Path $templateDest) {
    Remove-Item -Recurse -Force $templateDest
}
New-Item -ItemType Directory -Path (Join-Path $templateDest "hooks") -Force | Out-Null

$defaultTemplate = Get-LabOsDefaultGitTemplate
if ($defaultTemplate) {
    Write-Host "Seeding template from: $defaultTemplate"
    Copy-Item -Path (Join-Path $defaultTemplate "*") -Destination $templateDest -Recurse -Force
    New-Item -ItemType Directory -Path (Join-Path $templateDest "hooks") -Force | Out-Null
} else {
    Write-Host "WARNING: system Git template not found; LabOS post-checkout only."
}

Copy-Item -Path $templateSrc -Destination (Join-Path $templateDest "hooks\post-checkout") -Force

git config --global init.templateDir $templateDest

Write-Host ""
Write-Host "Verification:"
Write-Host "  init.templateDir: $(git config --global --get init.templateDir)"
Write-Host "  pre-commit: $((& pre-commit --version 2>$null) -join ' ')"
Write-Host "  trufflehog: $(((& trufflehog --version 2>$null) | Select-Object -First 1))"
Write-Host ""
Write-Host "Later clones of repos with .scan-gate/ will auto-run install (Husky repos skip pre-commit install)."

if ($SkipHooks) {
    Write-Host "Skipped hook install (-SkipHooks). From a repo root: .scan-gate\repository-bootstrap.ps1"
} elseif (Test-Path ".scan-gate\install-hooks.ps1") {
    & (Join-Path (Get-Location) ".scan-gate\install-hooks.ps1")
} else {
    Write-Host "No .scan-gate/install-hooks.ps1 in $(Get-Location) - git template is set; cd to a repo root to register this clone."
}
