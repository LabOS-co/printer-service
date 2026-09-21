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

$arguments = @(
    "filesystem",
    "--config", ".trufflehog.yaml",
    "--results=verified,unknown,unverified",
    "--fail",
    "--no-update",
    "--force-skip-binaries"
) + $Files

& trufflehog @arguments
exit $LASTEXITCODE
