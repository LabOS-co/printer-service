[CmdletBinding()]
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$Files
)

$ErrorActionPreference = "Stop"

if (-not $Files -or $Files.Count -eq 0) {
    exit 0
}

if (-not (Get-Command trufflehog -ErrorAction SilentlyContinue)) {
    [Console]::Error.WriteLine("trufflehog is required but was not found in PATH.")
    [Console]::Error.WriteLine("Install it from https://docs.trufflesecurity.com/pre-commit-hooks and retry.")
    exit 127
}

. (Join-Path $PSScriptRoot "trufflehog-excludes.ps1")

$kept = @(Select-TrufflehogPaths -Files $Files)
if ($kept.Count -eq 0) {
    exit 0
}

$arguments = @(
    "filesystem",
    "--config", ".trufflehog.yaml"
) + @(Get-TrufflehogExcludeCliArgs) + @(
    "--results=verified,unknown,unverified",
    "--no-update",
    "--force-skip-binaries"
) + $kept

$code = Invoke-TrufflehogFiltered -ArgumentList $arguments
exit $code
