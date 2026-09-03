#!/bin/bash
# Runs `printersearch bench` while sampling two things every ~10ms, so the
# result compares apples-to-apples against the Windows-side measurements
# (win-bench.ps1: spoolsv.exe = orchestrator, SumatraPDF.exe = renderer):
#   - cupsd itself (the orchestrator, analogous to spoolsv.exe)
#   - any short-lived filter/renderer child processes CUPS forks per job to
#     actually convert PDF -> raster (gs, pdftopdf, gstoraster, rastertopwg,
#     etc.) - analogous to SumatraPDF.exe on the Windows side.
set -uo pipefail

# BASE comes from this script's own location, not a hardcoded path that has
# been stale (and case-typo'd) since the working copy moved to
# C:\GitProjects\printer-server - see setup-emulators.sh's comment.
BASE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$BASE/../printersearch" || exit 1

CUPSD_PID=$(pgrep -x cupsd | head -1)
if [ -z "$CUPSD_PID" ]; then
  echo "cupsd not running"
  exit 1
fi

FILTER_PATTERN='^(gs|pdftopdf|gstoraster|rastertopwg|gziptoany|pstoraster|cfFilter.*|foomatic-rip)$'

CUPSD_SAMPLES=/tmp/cups-resource-samples.txt
FILTER_SAMPLES=/tmp/cups-filter-samples.txt
: > "$CUPSD_SAMPLES"
: > "$FILTER_SAMPLES"
HZ=$(getconf CLK_TCK)

sampler() {
  while true; do
    now=$(date +%s.%N)
    if [ -r "/proc/$CUPSD_PID/stat" ]; then
      read -r _ _ _ _ _ _ _ _ _ _ _ _ _ utime stime _ < "/proc/$CUPSD_PID/stat"
      rss_kb=$(awk '/VmRSS/{print $2}' "/proc/$CUPSD_PID/status" 2>/dev/null)
      echo "$now $utime $stime $rss_kb" >> "$CUPSD_SAMPLES"
    fi
    for p in /proc/[0-9]*; do
      pid=${p#/proc/}
      comm=$(cat "$p/comm" 2>/dev/null) || continue
      [[ "$comm" =~ $FILTER_PATTERN ]] || continue
      read -r _ _ _ _ _ _ _ _ _ _ _ _ _ utime stime _ < "$p/stat" 2>/dev/null || continue
      rss_kb=$(awk '/VmRSS/{print $2}' "$p/status" 2>/dev/null)
      echo "$now $pid $comm $utime $stime $rss_kb" >> "$FILTER_SAMPLES"
    done
    sleep 0.01
  done
}

sampler &
SAMPLER_PID=$!

# Without this, Ctrl-C (or any early exit - a bad flag rejected by
# `printersearch bench` itself, since that failure is deliberately NOT fatal
# to this script - see `set -uo pipefail` above, no `-e`) orphaned the 10ms
# sampler loop, left running forever until the shell that started it exited.
# Idempotent against the explicit kill/wait below on the normal path: a
# process already reaped just makes kill/wait fail quietly.
cleanup_sampler() {
  kill "$SAMPLER_PID" 2>/dev/null || true
  wait "$SAMPLER_PID" 2>/dev/null || true
}
trap cleanup_sampler EXIT

./printersearch bench -file testdata/printDemo.pdf "$@"
BENCH_EXIT=$?

# jobs are accepted asynchronously - give background filters (gs, etc.) time
# to actually spawn and finish before stopping the sampler and reporting.
sleep 8

cleanup_sampler

echo ""
echo "=== cupsd + filter/renderer resource usage during the run ==="
python3 - "$CUPSD_SAMPLES" "$FILTER_SAMPLES" "$HZ" <<'PYEOF'
import sys
cupsd_path, filter_path, hz = sys.argv[1], sys.argv[2], int(sys.argv[3])

rows = []
with open(cupsd_path) as f:
    for line in f:
        parts = line.split()
        if len(parts) != 4:
            continue
        t, utime, stime, rss = parts
        rows.append((float(t), int(utime), int(stime), int(rss) if rss.isdigit() else 0))

if len(rows) < 2:
    print(f"{'cupsd':<12} not enough samples captured ({len(rows)})")
else:
    cpu_ticks_delta = (rows[-1][1] + rows[-1][2]) - (rows[0][1] + rows[0][2])
    cpu_sec = cpu_ticks_delta / hz
    peak_rss_mb = max(r[3] for r in rows) / 1024
    avg_rss_mb = sum(r[3] for r in rows) / len(rows) / 1024
    print(f"{'cupsd':<12} samples={len(rows):<5} peak_mem={peak_rss_mb:8.1f}MB avg_mem={avg_rss_mb:8.1f}MB cpu_sec_during_run={cpu_sec:.2f}")

frows = []
with open(filter_path) as f:
    for line in f:
        parts = line.split()
        if len(parts) != 6:
            continue
        t, pid, comm, utime, stime, rss = parts
        frows.append((float(t), pid, comm, int(utime) + int(stime), int(rss) if rss.isdigit() else 0))

if not frows:
    print(f"{'filters':<12} no renderer/filter processes observed (job may have been passed through without local conversion)")
else:
    from collections import defaultdict
    by_pid_max_cpu = defaultdict(int)
    for _, pid, _, cpu, _ in frows:
        by_pid_max_cpu[pid] = max(by_pid_max_cpu[pid], cpu)
    total_cpu_sec = sum(by_pid_max_cpu.values()) / hz

    by_time_mem = defaultdict(int)
    for t, pid, _, _, rss in frows:
        by_time_mem[t] += rss
    peak_concurrent_mem_mb = max(by_time_mem.values()) / 1024

    names = sorted(set(comm for _, _, comm, _, _ in frows))
    print(f"{'filters':<12} instances={len(by_pid_max_cpu):<4} peak_concurrent_mem={peak_concurrent_mem_mb:8.1f}MB total_cpu_sec={total_cpu_sec:.2f} ({','.join(names)})")
PYEOF

exit $BENCH_EXIT
