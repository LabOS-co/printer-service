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

& trufflehog git file://. `
    --config .trufflehog.yaml `
    --since-commit "$SinceRef" `
    --results=verified,unknown,unverified `
    --fail `
    --no-update

exit $LASTEXITCODE
