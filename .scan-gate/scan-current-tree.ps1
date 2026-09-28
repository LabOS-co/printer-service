[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

if (-not (Get-Command trufflehog -ErrorAction SilentlyContinue)) {
    [Console]::Error.WriteLine("trufflehog is required but was not found in PATH.")
    exit 127
}

. (Join-Path $PSScriptRoot "trufflehog-excludes.ps1")

$excludeFile = New-TemporaryFile

try {
    Set-Content -Path $excludeFile -Value '^\.git/' -NoNewline
    if (Test-Path -LiteralPath ".trufflehog-exclude-paths") {
        Add-Content -Path $excludeFile -Value ""
        Get-Content -LiteralPath ".trufflehog-exclude-paths" | Add-Content -Path $excludeFile
    }

    $thArgs = @(
        "filesystem", ".",
        "--config", ".trufflehog.yaml",
        "--exclude-paths", "$excludeFile",
        "--results=verified,unknown,unverified",
        "--no-update",
        "--force-skip-binaries"
    )
    $code = Invoke-TrufflehogFiltered -ArgumentList $thArgs
    exit $code
}
finally {
    Remove-Item -Path $excludeFile -Force -ErrorAction SilentlyContinue
}
