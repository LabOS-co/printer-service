[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

function Test-LabOsScanGateSkip {
    return ($env:CI -eq 'true') `
        -or [bool]$env:JENKINS_URL `
        -or ($env:GITHUB_ACTIONS -eq 'true') `
        -or ($env:LABOS_SCAN_GATE_SKIP -eq '1')
}

function Add-LabOsPathDir {
    param([string]$Dir)
    if (-not $Dir) { return }
    if (-not (Test-Path $Dir)) {
        New-Item -ItemType Directory -Path $Dir -Force | Out-Null
    }
    $parts = $env:PATH -split ';' | Where-Object { $_ -ne '' }
    if ($parts -notcontains $Dir) {
        $env:PATH = "$Dir;$env:PATH"
    }
}

function Get-LabOsPythonScriptsDir {
    try {
        if (Get-Command py -ErrorAction SilentlyContinue) {
            return (& py -c "import sysconfig; print(sysconfig.get_path('scripts', 'nt_user'))")
        }
        if (Get-Command python -ErrorAction SilentlyContinue) {
            return (& python -c "import sysconfig; print(sysconfig.get_path('scripts', 'nt_user'))")
        }
    } catch {
        return $null
    }
    return $null
}

function Save-LabOsPathProfile {
    $marker = '# LabOS scan-gate PATH'
    $pyScripts = Get-LabOsPythonScriptsDir
    $pathAssign = '$env:PATH = "$env:USERPROFILE\bin;$env:USERPROFILE\.local\bin;'
    if ($pyScripts) {
        $pathAssign += $pyScripts.Trim() + ';'
    }
    $pathAssign += '$env:PATH"'

    $profilePath = $null
    if ($env:LABOS_SCAN_GATE_PROFILE_DIR) {
        $profileDir = $env:LABOS_SCAN_GATE_PROFILE_DIR
        if (-not (Test-Path $profileDir)) {
            New-Item -ItemType Directory -Path $profileDir -Force | Out-Null
        }
        $profilePath = Join-Path $profileDir "Microsoft.PowerShell_profile.ps1"
    } elseif ($PROFILE) {
        $profilePath = $PROFILE
        $profileDir = Split-Path $profilePath
        if ($profileDir -and -not (Test-Path $profileDir)) {
            New-Item -ItemType Directory -Path $profileDir -Force | Out-Null
        }
    } else {
        return
    }

    if (-not (Test-Path $profilePath)) {
        New-Item -ItemType File -Path $profilePath -Force | Out-Null
    }
    $existing = Get-Content $profilePath -Raw -ErrorAction SilentlyContinue
    if ($existing -and $existing.Contains($marker)) {
        return
    }
    Add-Content -Path $profilePath -Value "`n$marker`n$pathAssign`n"
    Write-Host "LABOS scan-gate: added PATH snippet to $profilePath (restart Cursor/VS Code)."
}

function Install-LabOsTrufflehogWindows {
    param([string]$DestDir)
    Add-LabOsPathDir $DestDir

    if (Get-Command bash -ErrorAction SilentlyContinue) {
        & bash -c 'curl -sSfL https://raw.githubusercontent.com/trufflesecurity/trufflehog/main/scripts/install.sh | sh -s -- -b "$HOME/bin"'
        if (Get-Command trufflehog -ErrorAction SilentlyContinue) {
            return
        }
        Write-Host "Official bash installer did not put trufflehog on PATH; trying GitHub release..."
    }

    $headers = @{ 'User-Agent' = 'LabOS-scan-gate' }
    $rel = Invoke-RestMethod -Uri 'https://api.github.com/repos/trufflesecurity/trufflehog/releases/latest' -Headers $headers
    $asset = $rel.assets | Where-Object { $_.name -match 'windows_amd64\.(tar\.gz|zip)$' } | Select-Object -First 1
    if (-not $asset) {
        throw "No windows_amd64 asset in the latest trufflehog release."
    }
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("trufflehog-dl-" + [guid]::NewGuid().ToString())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $archive = Join-Path $tmp $asset.name
        Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $archive -Headers $headers -UseBasicParsing
        if ($asset.name -like '*.zip') {
            Expand-Archive -Path $archive -DestinationPath $tmp -Force
        } else {
            tar -xf $archive -C $tmp
        }
        $exe = Get-ChildItem -Path $tmp -Filter trufflehog.exe -Recurse | Select-Object -First 1
        if (-not $exe) {
            throw "trufflehog.exe not found in release archive $($asset.name)."
        }
        Copy-Item $exe.FullName (Join-Path $DestDir "trufflehog.exe") -Force
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}

if (Test-LabOsScanGateSkip) {
    Write-Output "LABOS scan-gate: skipping tool auto-install (CI/automation)."
    exit 0
}

$userBin = Join-Path $env:USERPROFILE "bin"
$localBin = Join-Path $env:USERPROFILE ".local\bin"
Add-LabOsPathDir $userBin
Add-LabOsPathDir $localBin
$pyScripts = Get-LabOsPythonScriptsDir
if ($pyScripts) { Add-LabOsPathDir $pyScripts }

$havePreCommit = $null -ne (Get-Command pre-commit -ErrorAction SilentlyContinue)
$haveTrufflehog = $null -ne (Get-Command trufflehog -ErrorAction SilentlyContinue)

if ($havePreCommit -and $haveTrufflehog) {
    Save-LabOsPathProfile
    Write-Host "pre-commit: $(& pre-commit --version)"
    Write-Host "trufflehog: $((& trufflehog --version 2>&1 | Select-Object -First 1))"
    exit 0
}

if (-not $havePreCommit) {
    if (Get-Command py -ErrorAction SilentlyContinue) {
        & py -m pip install --user pre-commit
        if ($LASTEXITCODE -ne 0) {
            & py -m pip install --user --break-system-packages pre-commit
        }
    } elseif (Get-Command python -ErrorAction SilentlyContinue) {
        & python -m pip install --user pre-commit
        if ($LASTEXITCODE -ne 0) {
            & python -m pip install --user --break-system-packages pre-commit
        }
    } else {
        Write-Error "Python not found. Install manually: py -m pip install --user pre-commit"
        exit 1
    }
}

$pyScripts = Get-LabOsPythonScriptsDir
if ($pyScripts) { Add-LabOsPathDir $pyScripts }

if (-not (Get-Command trufflehog -ErrorAction SilentlyContinue)) {
    try {
        Install-LabOsTrufflehogWindows -DestDir $userBin
        Add-LabOsPathDir $userBin
    } catch {
        Write-Error "trufflehog install failed: $_"
        Write-Error "Download trufflehog.exe from https://github.com/trufflesecurity/trufflehog/releases into $userBin"
        exit 1
    }
}

$missing = $false
if (-not (Get-Command pre-commit -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: pre-commit is still not on PATH after install." -ForegroundColor Red
    Write-Host "Add `$env:USERPROFILE\bin and Python user Scripts to your PowerShell profile PATH."
    $missing = $true
}
if (-not (Get-Command trufflehog -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: trufflehog is still not on PATH after install." -ForegroundColor Red
    $missing = $true
}

if ($missing) {
    exit 1
}

Save-LabOsPathProfile
Write-Host "pre-commit: $(& pre-commit --version)"
Write-Host "trufflehog: $((& trufflehog --version 2>&1 | Select-Object -First 1))"
