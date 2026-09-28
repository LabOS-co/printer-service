[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$SinceRef
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($SinceRef)) {
    [Console]::Error.WriteLine("Usage: .scan-gate/scan-git-range.ps1 <since-commit-or-ref>")
    exit 2
}

if (-not (Get-Command trufflehog -ErrorAction SilentlyContinue)) {
    [Console]::Error.WriteLine("trufflehog is required but was not found in PATH.")
    exit 127
}

. (Join-Path $PSScriptRoot "trufflehog-excludes.ps1")

$thArgs = @(
    "git", "file://.",
    "--config", ".trufflehog.yaml"
) + @(Get-TrufflehogExcludeCliArgs) + @(
    "--since-commit", $SinceRef,
    "--results=verified,unknown,unverified",
    "--no-update"
)
$code = Invoke-TrufflehogFiltered -ArgumentList $thArgs
exit $code
