# $PSScriptRoot is this script's own directory - used to default $File
# relative to the actual checkout location instead of a hardcoded
# C:\printerSearch path that has been stale (and case-typo'd, HDL vs HLD)
# since the working copy moved to C:\GitProjects\printer-server.
param(
    [string]$File = (Join-Path (Split-Path $PSScriptRoot -Parent) "WSL\printDemo.pdf"),
    [string]$Printer = "BenchFilePrinter",
    [string]$Sumatra = "\\gaia\netlims$\AutoLims\MainRls\bin\SumatraPDF.exe",
    [int]$Requests = 30,
    [int]$Concurrency = 3
)

Write-Host "win-bench: $Requests requests, concurrency=$Concurrency, printer=$Printer, file=$File"

# --- submit jobs with bounded concurrency, timing each one, sampling
#     spoolsv.exe/SumatraPDF.exe resource usage inline on every poll tick
#     (a separate Start-Job was tried first but its own startup lag meant it
#     never got a sample in before short runs finished - sampling inline in
#     the same loop that already polls for job completion avoids that) ---
$latencies = New-Object System.Collections.ArrayList
$samples = New-Object System.Collections.ArrayList
$running = @()  # each entry: @{ Process=...; Stopwatch=... }
$submitted = 0
$overallSw = [System.Diagnostics.Stopwatch]::StartNew()

while ($submitted -lt $Requests -or $running.Count -gt 0) {
    while ($running.Count -lt $Concurrency -and $submitted -lt $Requests) {
        $psi = New-Object System.Diagnostics.ProcessStartInfo
        $psi.FileName = $Sumatra
        $psi.Arguments = "-print-to `"$Printer`" -silent `"$File`""
        $psi.UseShellExecute = $false
        $psi.CreateNoWindow = $true
        $sw = [System.Diagnostics.Stopwatch]::StartNew()
        $proc = [System.Diagnostics.Process]::Start($psi)
        $running += [pscustomobject]@{ Process = $proc; Stopwatch = $sw }
        $submitted++
    }

    $now = Get-Date
    foreach ($p in (Get-Process -Name spoolsv, SumatraPDF -ErrorAction SilentlyContinue)) {
        [void]$samples.Add([pscustomobject]@{
            Name    = $p.ProcessName
            Pid     = $p.Id
            CPU     = $p.CPU
            WSBytes = $p.WorkingSet64
            Time    = $now
        })
    }

    Start-Sleep -Milliseconds 20
    $stillRunning = @()
    foreach ($r in $running) {
        if ($r.Process.HasExited) {
            $r.Stopwatch.Stop()
            [void]$latencies.Add($r.Stopwatch.Elapsed.TotalMilliseconds)
        } else {
            $stillRunning += $r
        }
    }
    $running = $stillRunning
}
$overallSw.Stop()

function Report-Latency($label, $values) {
    if ($values.Count -eq 0) { return }
    $sorted = $values | Sort-Object
    $n = $sorted.Count
    $avg = ($sorted | Measure-Object -Average).Average
    $p50 = $sorted[[int](($n - 1) * 0.50)]
    $p95 = $sorted[[int](($n - 1) * 0.95)]
    "{0,-10} count={1,-4} min={2,8:N1}ms avg={3,8:N1}ms p50={4,8:N1}ms p95={5,8:N1}ms max={6,8:N1}ms" -f `
        $label, $n, $sorted[0], $avg, $p50, $p95, $sorted[$n - 1] | Write-Host
}

Write-Host ""
Write-Host "=== latency ==="
Report-Latency "sumatra" $latencies
Write-Host ("total wall time: {0:N1}ms, throughput: {1:N1} req/s" -f $overallSw.Elapsed.TotalMilliseconds, ($Requests / $overallSw.Elapsed.TotalSeconds))

Write-Host ""
Write-Host "=== resource usage during the run ==="

# spoolsv.exe is one long-lived process: CPU cost attributable to this run
# is the delta between its cumulative CPU time at the first and last sample.
$spoolSamples = $samples | Where-Object { $_.Name -eq "spoolsv" } | Sort-Object Time
if ($spoolSamples.Count -ge 2) {
    $cpuDelta = ($spoolSamples[-1].CPU - $spoolSamples[0].CPU)
    $peakWS = ($spoolSamples | Measure-Object -Property WSBytes -Maximum).Maximum
    $avgWS = ($spoolSamples | Measure-Object -Property WSBytes -Average).Average
    "{0,-12} samples={1,-5} peak_mem={2,8:N1}MB avg_mem={3,8:N1}MB cpu_sec_during_run={4:N2}" -f `
        "spoolsv", $spoolSamples.Count, ($peakWS / 1MB), ($avgWS / 1MB), $cpuDelta | Write-Host
} else {
    Write-Host "spoolsv: not enough samples captured"
}

# SumatraPDF is one short-lived process per job: total CPU cost is the sum
# of each instance's own final (max) cumulative CPU sample; peak memory is
# the largest sum of concurrently-alive instances' working sets at any one
# sampled instant (reflects real concurrent memory pressure, not one process).
$sumatraSamples = $samples | Where-Object { $_.Name -eq "SumatraPDF" }
if ($sumatraSamples.Count -gt 0) {
    $perPidMaxCpu = $sumatraSamples | Group-Object Pid | ForEach-Object {
        ($_.Group | Measure-Object -Property CPU -Maximum).Maximum
    }
    $totalCpu = ($perPidMaxCpu | Measure-Object -Sum).Sum
    $peakConcurrentMem = $sumatraSamples | Group-Object Time | ForEach-Object {
        ($_.Group | Measure-Object -Property WSBytes -Sum).Sum
    } | Measure-Object -Maximum
    $instanceCount = ($sumatraSamples | Select-Object -ExpandProperty Pid -Unique).Count
    "{0,-12} instances={1,-4} peak_concurrent_mem={2,8:N1}MB total_cpu_sec={3:N2}" -f `
        "SumatraPDF", $instanceCount, ($peakConcurrentMem.Maximum / 1MB), $totalCpu | Write-Host
} else {
    Write-Host "SumatraPDF: no samples captured (jobs may have completed faster than the 100ms sampling interval)"
}
