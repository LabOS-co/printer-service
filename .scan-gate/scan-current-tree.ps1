[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

if (-not (Get-Command trufflehog -ErrorAction SilentlyContinue)) {
    [Console]::Error.WriteLine("trufflehog is required but was not found in PATH.")
    exit 127
}

$excludeFile = New-TemporaryFile

try {
    Set-Content -Path $excludeFile -Value '^\.git/' -NoNewline

    & trufflehog filesystem . `
        --config .trufflehog.yaml `
        --exclude-paths "$excludeFile" `
        --results=verified,unknown,unverified `
        --fail `
        --no-update `
        --force-skip-binaries

    exit $LASTEXITCODE
}
finally {
    Remove-Item -Path $excludeFile -Force -ErrorAction SilentlyContinue
}
