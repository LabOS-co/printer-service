[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = (& git rev-parse --show-toplevel 2>$null)
if ($repoRoot) {
    Set-Location $repoRoot.Trim()
}

$userBin = Join-Path $env:USERPROFILE "bin"
$localBin = Join-Path $env:USERPROFILE ".local\bin"
$env:PATH = "$userBin;$localBin;$env:PATH"

if (-not (Get-Command trufflehog -ErrorAction SilentlyContinue)) {
    $ensure = Join-Path (Get-Location) ".scan-gate\ensure-tools.ps1"
    if (Test-Path $ensure) {
        try { & $ensure } catch { }
        $env:PATH = "$userBin;$localBin;$env:PATH"
    }
}

if (-not (Get-Command trufflehog -ErrorAction SilentlyContinue)) {
    [Console]::Error.WriteLine("trufflehog is required but was not found in PATH.")
    [Console]::Error.WriteLine("Run .scan-gate/ensure-tools.ps1 or .scan-gate/repository-bootstrap.ps1.")
    exit 127
}

$zeroRef = "0000000000000000000000000000000000000000"

function Invoke-ScanSince([string]$Base) {
    Write-Host "Running TruffleHog git scan since $Base..."
    & trufflehog git file://. `
        --config .trufflehog.yaml `
        --since-commit $Base `
        --results=verified,unknown,unverified `
        --fail `
        --no-update
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

$fromRef = $env:PRE_COMMIT_FROM_REF
if ($fromRef -and $fromRef -ne $zeroRef) {
    & git cat-file -e "$fromRef^{commit}" 2>$null
    if ($LASTEXITCODE -eq 0) {
        Invoke-ScanSince $fromRef
        exit 0
    }
}

$scanned = $false
$inputLines = @($input)
foreach ($line in $inputLines) {
    if (-not $line) { continue }
    $parts = $line.Trim() -split '\s+'
    if ($parts.Count -lt 4) { continue }
    $localSha = $parts[1]
    $remoteSha = $parts[3]
    if ($localSha -eq $zeroRef) { continue }
    $base = $remoteSha
    if (-not $base -or $base -eq $zeroRef) {
        & git rev-parse --verify origin/dev 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) {
            $base = (& git merge-base $localSha origin/dev 2>$null)
        }
        if (-not $base) {
            Write-Host "New branch: no merge-base with origin/dev; skipping history-wide scan."
            continue
        }
    }
    Invoke-ScanSince $base
    $scanned = $true
}

if (-not $scanned) {
    Write-Host "No push range; skipping TruffleHog pre-push scan."
    exit 0
}
exit 0
