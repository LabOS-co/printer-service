# Path skips for built-in TruffleHog detectors.
# exclude_words in .trufflehog.yaml applies only to custom detectors.
# Repo-root file: .trufflehog-exclude-paths

function Get-TrufflehogExcludeCliArgs {
    if (Test-Path -LiteralPath ".trufflehog-exclude-paths") {
        return @("--exclude-paths", ".trufflehog-exclude-paths")
    }
    return @()
}

function Select-TrufflehogPaths {
    param([string[]]$Files)

    if (-not $Files) {
        return @()
    }
    if (-not (Test-Path -LiteralPath ".trufflehog-exclude-paths")) {
        return @($Files)
    }

    $patterns = @()
    foreach ($line in Get-Content -LiteralPath ".trufflehog-exclude-paths") {
        $trimmed = "$line".Trim()
        if ($trimmed -eq "" -or $trimmed.StartsWith("#")) {
            continue
        }
        $patterns += $trimmed
    }

    $kept = @()
    foreach ($f in $Files) {
        $normalized = $f -replace '\\', '/'
        $skip = $false
        foreach ($pattern in $patterns) {
            if ($normalized -match $pattern) {
                $skip = $true
                break
            }
        }
        if ($skip) {
            Write-Host "TruffleHog: path excluded by .trufflehog-exclude-paths: $f"
        } else {
            $kept += $f
        }
    }
    return @($kept)
}

function Invoke-TrufflehogFiltered {
    param([string[]]$ArgumentList)

    $errFile = New-TemporaryFile
    try {
        $lines = & trufflehog @ArgumentList --json 2>$errFile.FullName
        $code = $LASTEXITCODE
        if (Test-Path -LiteralPath $errFile.FullName) {
            Get-Content -LiteralPath $errFile.FullName -ErrorAction SilentlyContinue | ForEach-Object {
                [Console]::Error.WriteLine($_)
            }
        }
        if ($code -ne 0) {
            return $code
        }

        $words = @()
        if (Test-Path -LiteralPath ".trufflehog-exclude-words") {
            foreach ($line in Get-Content -LiteralPath ".trufflehog-exclude-words") {
                $trimmed = "$line".Trim()
                if ($trimmed -and -not $trimmed.StartsWith("#")) {
                    $words += $trimmed
                }
            }
        }

        $kept = 0
        $ignored = 0
        foreach ($line in @($lines)) {
            $text = "$line".Trim()
            if (-not $text.StartsWith("{")) { continue }
            try {
                $obj = $text | ConvertFrom-Json
            } catch {
                $kept++
                [Console]::Error.WriteLine("TruffleHog: unparsed finding, blocking")
                continue
            }
            if (-not $obj.DetectorName -and -not $obj.Raw) { continue }

            $blob = "$($obj.Raw)`n$($obj.RawV2)"
            $drop = $false
            foreach ($word in $words) {
                if ($blob.Contains($word)) {
                    $drop = $true
                    break
                }
            }
            if ($drop) {
                $ignored++
                continue
            }

            $kept++
            $file = $null
            $lineno = $null
            $data = $obj.SourceMetadata.Data
            if ($data.Git) {
                $file = $data.Git.file
                $lineno = $data.Git.line
            } elseif ($data.Filesystem) {
                $file = $data.Filesystem.file
                $lineno = $data.Filesystem.line
            }
            [Console]::Error.WriteLine("Detector: $($obj.DetectorName)")
            [Console]::Error.WriteLine("File: $file")
            [Console]::Error.WriteLine("Line: $lineno")
            if ($obj.Redacted) {
                [Console]::Error.WriteLine("Redacted: $($obj.Redacted)")
            }
            [Console]::Error.WriteLine("")
        }
        if ($ignored -gt 0) {
            [Console]::Error.WriteLine("TruffleHog: ignored $ignored finding(s) matching .trufflehog-exclude-words")
        }
        if ($kept -gt 0) { return 183 }
        return 0
    }
    finally {
        Remove-Item -LiteralPath $errFile.FullName -Force -ErrorAction SilentlyContinue
    }
}
