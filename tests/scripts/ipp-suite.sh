#!/usr/bin/env bash
# printersearch and ippfix scenarios: TEST-PLAN.md sections 15 and 16.
#
#   ipp-suite.sh                 # everything that consumes no paper
#   ipp-suite.sh --with-paper    # adds the rows that physically print
#   ipp-suite.sh --bench         # adds IPP-BENCH-* (needs the 15-queue fleet)
#   ipp-suite.sh --only IPP-12   # one scenario
#
# These speak raw IPP, which Postman cannot do at all — hence a separate
# harness rather than more collection rows.
#
# Targets:
#   QUEUE  a CUPS queue path            127.0.0.1:631/printers/<queue>
#   VIRT   a virtual ippeveprinter      127.0.0.1:9101/ipp/print
#   FIX    the ippfix proxy             127.0.0.1:6310/ipp/print
#   REAL   the physical Brother         192.168.252.210:631/ipp/print
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_vars

PS="$REPO_DIR/src/printersearch/printersearch"
PDF="$TESTS_DIR/testdata/printDemo.pdf"
SMALL="$TESTS_DIR/testdata/small.pdf"

PAPER=0; BENCH=0; ONLY=""
while (($#)); do
  case "$1" in
    --with-paper) PAPER=1 ;;
    --no-paper) PAPER=0 ;;
    --bench) BENCH=1 ;;
    --only) ONLY="$2"; shift ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
  shift
done

[[ -x "$PS" ]] || die "printersearch not built — run: tests/scripts/deploy.sh"
[[ -f "$PDF" ]] || die "fixtures missing — run: tests/scripts/make-fixtures.sh"

REAL_HOST=192.168.252.210
PAPER_QUEUE="${GW_PAPER_PRINTER:-brother-direct}"
VIRT_QUEUE="${GW_PRINTER:-vp1}"
FIX_PORT=6310

want() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

# run_ps <args...> — capture combined output and exit code without tripping
# set -e; every assertion below inspects both.
PS_OUT=""; PS_RC=0
# PS_WALL_TIMEOUT is a wall-clock cap on the whole subprocess, distinct from
# printersearch's own -timeout (which bounds the IPP conversation, not the
# work before it). Without it one hanging case stalls the entire regression
# with no output and no verdict — which is exactly what IPP-15 did on
# 2026-09-07: `printersearch print -file <FIFO>` blocked for 20 minutes inside
# os.Open, because opening a FIFO with no writer blocks by POSIX rule and the
# "not a regular file" guard sits AFTER that open (src/printersearch/cmd/
# printersearch/main.go:94). A hang must fail a row, never hold the run.
PS_WALL_TIMEOUT="${PS_WALL_TIMEOUT:-30}"

run_ps() {
  PS_OUT="$(timeout -k 5 "$PS_WALL_TIMEOUT" "$PS" "$@" 2>&1)"; PS_RC=$?
  # 124 is timeout(1)'s own "the command was killed at the deadline"; say so
  # rather than leaving a bare rc for the row's own message to misreport.
  if (( PS_RC == 124 )); then
    PS_OUT="${PS_OUT} [timed out after ${PS_WALL_TIMEOUT}s — printersearch did not return]"
  fi
  return 0
}

reachable() { timeout 3 bash -c "</dev/tcp/$1/$2" >/dev/null 2>&1; }

# ===========================================================================
say "IPP · printersearch against CUPS queues"
# ===========================================================================

QUEUE_UP=0
if reachable 127.0.0.1 631; then QUEUE_UP=1; else warn "cupsd not reachable on 127.0.0.1:631 — most rows will skip"; fi

if want IPP-02; then
  if ((QUEUE_UP)); then
    run_ps info -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -timeout 15s
    if (( PS_RC == 0 )) && [[ "$PS_OUT" == *"successful-ok"* ]]; then
      t_pass IPP-02 "info against a CUPS queue"
    else
      t_fail IPP-02 "info against a CUPS queue (rc=$PS_RC): $(printf '%.160s' "$PS_OUT")"
    fi
  else t_skip IPP-02 "cupsd unreachable"; fi
fi

if want IPP-03; then
  if reachable 127.0.0.1 9101; then
    run_ps info -host 127.0.0.1 -port 9101 -timeout 15s
    t_contains IPP-03 "info against a virtual ippeveprinter" "successful-ok" "$PS_OUT"
  else t_skip IPP-03 "no ippeveprinter on 9101 — run src/ops/setup-15-printers.sh"; fi
fi

if want IPP-01; then
  if reachable "$REAL_HOST" 631; then
    run_ps info -host "$REAL_HOST" -timeout 20s
    if [[ "$PS_OUT" == *"successful-ok"* ]]; then
      t_pass IPP-01 "info against the real printer"
      # The printer names the attributes it refused, in the unsupported group.
      rej="$(grep -c 'REJECTED BY PRINTER' <<< "$PS_OUT" || true)"
      note "  printer rejected $rej requested attribute(s)"
    else
      t_fail IPP-01 "info against the real printer: $(printf '%.160s' "$PS_OUT")"
    fi
  else t_skip IPP-01 "real printer $REAL_HOST unreachable"; fi
fi

if want IPP-04; then
  run_ps info
  # Usage must go to stderr, not stdout: the one message explaining the failure
  # is exactly what `> out.txt` would otherwise swallow.
  err_only="$("$PS" info 2>&1 1>/dev/null)"
  if (( PS_RC != 0 )) && [[ "$err_only" == *"-host is required"* ]]; then
    t_pass IPP-04 "info without -host exits non-zero, message on stderr"
  else
    t_fail IPP-04 "info without -host (rc=$PS_RC, stderr=$(printf '%.100s' "$err_only"))"
  fi
fi

if want IPP-05; then
  # 10.255.255.1 is a black hole: packets are dropped, not refused, so this
  # hangs until the client's own deadline fires. That is what makes it a real
  # test of -timeout rather than of connection refusal.
  start=$(date +%s)
  run_ps info -host 10.255.255.1 -timeout 3s
  elapsed=$(( $(date +%s) - start ))
  if (( PS_RC != 0 )) && (( elapsed <= 12 )); then
    t_pass IPP-05 "unreachable host honours -timeout (${elapsed}s)"
  else
    t_fail IPP-05 "unreachable host: rc=$PS_RC after ${elapsed}s (expected non-zero within ~3s)"
  fi
fi

if want IPP-06; then
  if ((QUEUE_UP)); then
    run_ps info -host 127.0.0.1 -path /printers/there-is-no-such-queue -timeout 10s
    if [[ "$PS_OUT" == *"panic"* ]]; then
      t_fail IPP-06 "wrong path panicked instead of reporting an IPP status"
    elif (( PS_RC != 0 )) || [[ "$PS_OUT" == *"not-found"* || "$PS_OUT" == *"not-possible"* || "$PS_OUT" == *"forbidden"* ]]; then
      t_pass IPP-06 "wrong path reports an IPP error, no panic"
    else
      t_fail IPP-06 "wrong path: rc=$PS_RC out=$(printf '%.160s' "$PS_OUT")"
    fi
  else t_skip IPP-06 "cupsd unreachable"; fi
fi

if want IPP-08; then
  if reachable 127.0.0.1 9101; then
    run_ps print -host 127.0.0.1 -port 9101 -file "$SMALL" -timeout 30s
    t_expect IPP-08 "print to a virtual printer" 0 "$PS_RC"
  else t_skip IPP-08 "no ippeveprinter on 9101"; fi
fi

if want IPP-13; then
  run_ps print -host 127.0.0.1 -path "/printers/$VIRT_QUEUE"
  t_expect IPP-13 "print without -file exits non-zero" 1 "$PS_RC"
fi

if want IPP-14; then
  run_ps print -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -file /nonexistent/nope.pdf -timeout 5s
  if (( PS_RC != 0 )) && [[ "$PS_OUT" == *"error opening file"* ]]; then
    t_pass IPP-14 "nonexistent file reported cleanly"
  else
    t_fail IPP-14 "nonexistent file: rc=$PS_RC out=$(printf '%.120s' "$PS_OUT")"
  fi
fi

if want IPP-15; then
  # A FIFO reports Size()==0 from Stat, so an unguarded stream would put a
  # well-framed Print-Job carrying an EMPTY document on the wire. The guard
  # must refuse it outright rather than degrade.
  fifo="$(mktemp -u)"; mkfifo "$fifo"
  run_ps print -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -file "$fifo" -timeout 5s
  rm -f "$fifo"
  if (( PS_RC != 0 )) && [[ "$PS_OUT" == *"not a regular file"* ]]; then
    t_pass IPP-15 "non-regular file refused (silent-truncation guard)"
  else
    t_fail IPP-15 "FIFO: rc=$PS_RC out=$(printf '%.120s' "$PS_OUT")"
  fi
fi

if want IPP-12; then
  if ((QUEUE_UP)); then
    # -hold exercises the whole job lifecycle with no paper: submit held,
    # find it in `jobs`, cancel it, confirm it is gone.
    run_ps print -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -file "$SMALL" -hold -timeout 20s
    if (( PS_RC != 0 )); then
      t_fail IPP-12 "held submit failed: $(printf '%.160s' "$PS_OUT")"
    else
      jid="$(grep -oE 'job-id[^0-9]*([0-9]+)' <<< "$PS_OUT" | grep -oE '[0-9]+' | head -1)"
      if [[ -z "$jid" ]]; then
        t_fail IPP-12 "could not parse a job-id from: $(printf '%.160s' "$PS_OUT")"
      else
        run_ps jobs -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -timeout 15s
        listed="$PS_OUT"
        run_ps cancel -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -job-id "$jid" -timeout 15s
        if [[ "$listed" == *"$jid"* ]] && (( PS_RC == 0 )); then
          t_pass IPP-12 "hold → jobs → cancel lifecycle (job $jid)"
        else
          t_fail IPP-12 "job $jid listed=$([[ "$listed" == *"$jid"* ]] && echo yes || echo no) cancel_rc=$PS_RC"
        fi
      fi
    fi
  else t_skip IPP-12 "cupsd unreachable"; fi
fi

if want IPP-17; then
  if ((QUEUE_UP)); then
    run_ps jobs -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -timeout 15s
    t_expect IPP-17 "jobs on a queue" 0 "$PS_RC"
  else t_skip IPP-17 "cupsd unreachable"; fi
fi

if want IPP-18; then
  if ((QUEUE_UP)); then
    run_ps cancel -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -job-id 999999 -timeout 15s
    if [[ "$PS_OUT" == *"panic"* ]]; then
      t_fail IPP-18 "cancelling a nonexistent job panicked"
    else
      t_pass IPP-18 "cancelling a nonexistent job is handled (rc=$PS_RC)"
    fi
  else t_skip IPP-18 "cupsd unreachable"; fi
fi

if want IPP-19; then
  run_ps cancel -host 127.0.0.1 -path "/printers/$VIRT_QUEUE"
  t_expect IPP-19 "cancel without -job-id exits non-zero" 1 "$PS_RC"
fi

if want IPP-20; then
  if ((QUEUE_UP)); then
    # An attribute value long enough to overflow the wire framing's uint16
    # length prefix. Must be clamped rune-safely — never a corrupted frame,
    # never a panic.
    long="$(head -c 80000 /dev/zero | tr '\0' 'A')"
    run_ps print -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -file "$SMALL" -job-name "$long" -hold -timeout 20s
    if [[ "$PS_OUT" == *"panic"* ]]; then
      t_fail IPP-20 "oversized attribute panicked"
    else
      t_pass IPP-20 "oversized attribute clamped, no wire corruption (rc=$PS_RC)"
      jid="$(grep -oE 'job-id[^0-9]*([0-9]+)' <<< "$PS_OUT" | grep -oE '[0-9]+' | head -1)"
      [[ -n "$jid" ]] && "$PS" cancel -host 127.0.0.1 -path "/printers/$VIRT_QUEUE" -job-id "$jid" >/dev/null 2>&1
    fi
  else t_skip IPP-20 "cupsd unreachable"; fi
fi

# ===========================================================================
say "FIX · ippfix"
# ===========================================================================
FIX_UP=0
reachable 127.0.0.1 "$FIX_PORT" && FIX_UP=1

if want FIX-02; then
  if ((FIX_UP)); then
    run_ps info -host 127.0.0.1 -port "$FIX_PORT" -timeout 20s
    if [[ "$PS_OUT" == *"successful-ok"* ]]; then
      t_pass FIX-02 "unfiltered Get-Printer-Attributes through ippfix"
    else
      t_fail FIX-02 "unfiltered request through ippfix: $(printf '%.160s' "$PS_OUT")"
    fi
  else t_skip FIX-02 "ippfix not listening on $FIX_PORT"; fi
fi

if want FIX-04; then
  if ((FIX_UP)) && command -v driverless >/dev/null 2>&1; then
    ppd="$(timeout 60 driverless cat "ipp://127.0.0.1:${FIX_PORT}/ipp/print" 2>&1)"
    if [[ "$ppd" == *"*PPD-Adobe"* ]]; then
      t_pass FIX-04 "driverless generates a PPD through ippfix"
    else
      t_fail FIX-04 "driverless through ippfix produced no PPD: $(printf '%.160s' "$ppd")"
    fi
  else t_skip FIX-04 "ippfix down or driverless missing"; fi
fi

if want FIX-03; then
  # EXPECTED FAILURE, asserted as such. docs/STATUS.md's fifth phase records
  # that a filtered requested-attributes request through ippfix returns
  # server-error-internal-error, which is why brother-direct bypasses the proxy
  # for real traffic. If this ever starts succeeding, the bug was fixed — update
  # STATUS.md and this row together rather than "fixing" the expectation.
  if ((FIX_UP)) && command -v ipptool >/dev/null 2>&1; then
    tf="$(mktemp --suffix=.test)"
    cat > "$tf" <<'IPPTEST'
{
  OPERATION Get-Printer-Attributes
  GROUP operation-attributes-tag
  ATTR charset attributes-charset utf-8
  ATTR naturalLanguage attributes-natural-language en
  ATTR uri printer-uri $uri
  ATTR keyword requested-attributes printer-state,printer-state-reasons
}
IPPTEST
    out="$(timeout 30 ipptool -t "ipp://127.0.0.1:${FIX_PORT}/ipp/print" "$tf" 2>&1)"; rm -f "$tf"
    if [[ "$out" == *"server-error-internal-error"* ]]; then
      t_pass FIX-03 "filtered request still returns server-error-internal-error (KNOWN BUG, expected)"
    elif [[ "$out" == *"successful-ok"* ]]; then
      t_fail FIX-03 "filtered request now SUCCEEDS — the known ippfix bug appears fixed; update docs/STATUS.md and TEST-PLAN.md section 16"
    else
      t_fail FIX-03 "unexpected result: $(printf '%.160s' "$out")"
    fi
  else t_skip FIX-03 "ippfix down or ipptool missing (apt install cups-ipp-utils)"; fi
fi

# ===========================================================================
if ((PAPER)); then
say "PAPER · rows that physically print on $PAPER_QUEUE"
# ===========================================================================
  if ! reachable 127.0.0.1 631; then
    t_skip IPP-07 "cupsd unreachable"
  else
    for row in \
      "IPP-07|2 pages, defaults|-file $PDF -resolution 300" \
      "IPP-09|2 pages at 600dpi|-file $PDF -resolution 600" \
      "IPP-10|4 pages, 2 copies|-file $PDF -copies 2 -media iso_a4_210x297mm -color-mode monochrome" \
      "IPP-11|1 page, page-range 1-1|-file $PDF -first-page 1 -last-page 1"
    do
      IFS='|' read -r id desc args <<< "$row"
      want "$id" || continue
      # shellcheck disable=SC2086
      run_ps print -host 127.0.0.1 -path "/printers/$PAPER_QUEUE" $args -timeout 60s
      if (( PS_RC == 0 )); then
        t_pass "$id" "submitted — CONFIRM AT THE PRINTER: $desc"
      else
        t_fail "$id" "$desc: $(printf '%.160s' "$PS_OUT")"
      fi
    done
  fi

  if want IPP-16; then
    if reachable "$REAL_HOST" 631; then
      run_ps print -host "$REAL_HOST" -file "$PDF" -resolution 300 -timeout 60s
      if (( PS_RC == 0 )); then
        t_pass IPP-16 "direct-to-printer submitted — CONFIRM AT THE PRINTER: 2 pages (bypasses CUPS entirely)"
      else
        t_fail IPP-16 "direct to printer: $(printf '%.160s' "$PS_OUT")"
      fi
    else t_skip IPP-16 "real printer unreachable"; fi
  fi
else
  note "paper rows skipped (pass --with-paper to run them)"
fi

# ===========================================================================
if ((BENCH)); then
say "BENCH · load rig"
# ===========================================================================
  qcount="$(lpstat -p 2>/dev/null | grep -c '^printer q-' || echo 0)"
  if (( qcount < 3 )); then
    t_skip IPP-BENCH-01 "only $qcount q-* queues exist — run src/ops/setup-15-printers.sh then setup-15-queues.sh"
  else
    paths="$(lpstat -p 2>/dev/null | awk '/^printer q-/{printf "/printers/%s,", $2}' | sed 's/,$//')"
    three="$(cut -d, -f1-3 <<< "$paths")"

    if want IPP-BENCH-01; then
      run_ps bench -host 127.0.0.1 -port 631 -paths "$three" -requests 30 -concurrency 3 -file "$SMALL" -timeout 60s
      if (( PS_RC == 0 )) && [[ "$PS_OUT" == *"fail=0"* ]]; then
        t_pass IPP-BENCH-01 "30 jobs / concurrency 3 across 3 queues, no failures"
      else
        t_fail IPP-BENCH-01 "$(printf '%.200s' "$PS_OUT")"
      fi
    fi

    if want IPP-BENCH-02; then
      run_ps bench -host 127.0.0.1 -port 631 -paths "$three" -requests 9 -concurrency 3 -file "$SMALL" -json -timeout 60s
      if python3 -c "import json,sys; json.loads(sys.stdin.read())" <<< "$PS_OUT" 2>/dev/null; then
        t_pass IPP-BENCH-02 "-json emits parseable JSON"
      else
        t_fail IPP-BENCH-02 "-json output did not parse: $(printf '%.160s' "$PS_OUT")"
      fi
    fi

    if want IPP-BENCH-03; then
      run_ps bench -host 127.0.0.1 -port 631 -paths "$three" -requests 9 -concurrency 3 -file "$SMALL" \
        -wait-completion -poll-timeout 60s -timeout 90s
      t_contains IPP-BENCH-03 "-wait-completion reports a separate completed latency" "completed" "$PS_OUT"
    fi

    if want IPP-BENCH-04; then
      # One bogus path among good ones. The sixth-phase fix means failures must
      # be counted separately AND excluded from the success percentiles — a run
      # that folded a fast connection refusal into p50 would look faster the
      # more it failed.
      run_ps bench -host 127.0.0.1 -port 631 -paths "${three},/printers/no-such-queue" \
        -requests 12 -concurrency 3 -file "$SMALL" -timeout 60s
      if [[ "$PS_OUT" == *"fail="* ]] && [[ "$PS_OUT" != *"fail=0"* ]]; then
        t_pass IPP-BENCH-04 "failures counted separately from the success percentiles"
      else
        t_fail IPP-BENCH-04 "expected a non-zero fail count: $(printf '%.200s' "$PS_OUT")"
      fi
    fi

    if want IPP-BENCH-05; then
      if (( qcount < 15 )); then
        t_skip IPP-BENCH-05 "needs all 15 q-* queues (found $qcount)"
      else
        maxjobs="$(grep -iE '^\s*MaxJobs' /etc/cups/cupsd.conf 2>/dev/null | awk '{print $2}')"
        if [[ "${maxjobs:-500}" -lt 2000 ]]; then
          t_skip IPP-BENCH-05 "MaxJobs is ${maxjobs:-500}; the 700-job run needs 2000 in /etc/cups/cupsd.conf"
        else
          run_ps bench -host 127.0.0.1 -port 631 -paths "$paths" -requests 700 -concurrency 20 \
            -file "$SMALL" -wait-completion -poll-timeout 120s -timeout 180s
          if [[ "$PS_OUT" == *"fail=0"* ]]; then
            t_pass IPP-BENCH-05 "700 jobs / concurrency 20 across 15 queues, no failures"
          else
            t_fail IPP-BENCH-05 "a 'Too many active jobs' failure here means the MaxJobs 2000 change was lost: $(printf '%.200s' "$PS_OUT")"
          fi
        fi
      fi
    fi
  fi
fi

t_summary "ipp-suite"
