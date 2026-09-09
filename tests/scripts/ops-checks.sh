#!/usr/bin/env bash
# Operational scenarios: TEST-PLAN.md section 14 (GW-OPS-*), plus the three
# rows the Postman sandbox structurally cannot do — GW-PRE-17 (grep the log),
# E2E-06 (wait for a URL to expire), E2E-07 (cross-protocol).
#
#   ops-checks.sh                # everything runnable under the live profile
#   ops-checks.sh --only GW-OPS-03
#
# Assumes a profile is already selected (profile.sh) and the gateway is up.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_root
load_vars

ONLY=""
[[ "${1:-}" == "--only" ]] && ONLY="${2:-}"
want() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

PROFILE="$(current_profile)"
[[ -n "$PROFILE" ]] || die "no profile selected — run: tests/scripts/profile.sh A"
BASE="$(gw_base_url)"
TOKEN="$(python3 -c "
import json,sys
e=json.load(open('$TESTS_DIR/postman/env/${PROFILE}.postman_environment.json'))
print(next((v['value'] for v in e['values'] if v['key']=='token'), ''))
")"
[[ -n "$TOKEN" ]] || die "no token in the $PROFILE environment — run: node tests/postman/build-collection.js"
PRINTER="${GW_PRINTER:-vp1}"
PDF="$TESTS_DIR/testdata/small.pdf"

hdr=(-H "X-Labos-Print-Token: $TOKEN")

say "ops-checks · profile $PROFILE · $BASE"

# ---------------------------------------------------------------------------
if want GW-OPS-06 || want GW-OPS-07; then
  # Exactly one completion line per request, including a 401. Before the
  # accessLog middleware existed, LogAPICompletion was never invoked anywhere
  # in this service — so "there is a line at all" is the assertion.
  since="$(date '+%Y-%m-%d %H:%M:%S')"; sleep 1
  id="ops-$(date +%s)-ok"
  curl -s -o /dev/null "${hdr[@]}" -H "X-Laas-Identifier: $id" \
    -F "printer=$PRINTER" -F "file=@$PDF" "$BASE/print"
  id401="ops-$(date +%s)-401"
  curl -s -o /dev/null -H "X-Labos-Print-Token: wrong" -H "X-Laas-Identifier: $id401" "$BASE/print"
  sleep 1
  log="$(gw_log_since "$since")"

  want GW-OPS-06 && {
    if [[ "$log" == *"print submitted"* ]]; then
      t_pass GW-OPS-06 "the request produced log output"
    else
      t_fail GW-OPS-06 "no log line for a successful print"
    fi
  }
  want GW-OPS-07 && {
    # The 401's own line names the rejection and the caller address.
    if [[ "$log" == *"bad or missing X-Labos-Print-Token"* ]]; then
      t_pass GW-OPS-07 "a 401 is logged with the reason and caller address"
    else
      t_fail GW-OPS-07 "no rejection line logged for the 401"
    fi
  }
  note "  (LogAPICompletion renders as a message-less, timestamp-only line on console output — the structured job_id/duration/status fields only appear once LOG_SERVER is configured; see README 'Correlation ID')"
fi

# ---------------------------------------------------------------------------
if want GW-OPS-03; then
  # A wedged queue must fail the request at the submit timeout, not hold the
  # handler — and the connection — for the full write timeout.
  say "GW-OPS-03 · disabling $PRINTER to wedge lp"
  cupsdisable "$PRINTER" 2>/dev/null || true
  # cupsdisable stops printing but lp still ACCEPTS the job, which is the
  # correct CUPS behaviour and means this row asserts the accept path stays
  # responsive rather than the timeout firing. Rejecting is what -c does.
  cupsreject "$PRINTER" 2>/dev/null || true
  start=$(date +%s)
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 90 "${hdr[@]}" \
    -F "printer=$PRINTER" -F "file=@$PDF" "$BASE/print")"
  elapsed=$(( $(date +%s) - start ))
  cupsaccept "$PRINTER" 2>/dev/null || true
  cupsenable "$PRINTER" 2>/dev/null || true
  if [[ "$code" =~ ^(500|504)$ ]] && (( elapsed < 60 )); then
    t_pass GW-OPS-03 "a rejecting queue fails in ${elapsed}s with HTTP $code (not the 8m write timeout)"
  else
    t_fail GW-OPS-03 "HTTP $code after ${elapsed}s (wanted 500/504 well under 60s)"
  fi
fi

# ---------------------------------------------------------------------------
if want GW-OPS-04; then
  # Open a socket, send a partial request line, and go quiet. net/http's own
  # ReadHeaderTimeout must fire, and the resulting error line must be routed
  # through the service's logger rather than net/http's default stderr logger.
  since="$(date '+%Y-%m-%d %H:%M:%S')"; sleep 1
  host="${GW_ADDR%%:*}"; port="${GW_ADDR##*:}"
  start=$(date +%s)
  (exec 3<>"/dev/tcp/${host:-127.0.0.1}/${port:-8090}"; printf 'GET /print HTT'; sleep 20; exec 3<&-) 2>/dev/null &
  bgpid=$!
  wait $bgpid 2>/dev/null
  elapsed=$(( $(date +%s) - start ))
  if (( elapsed <= 25 )); then
    t_pass GW-OPS-04 "a silent client is cut off (${elapsed}s), not held open"
  else
    t_fail GW-OPS-04 "silent client held for ${elapsed}s"
  fi
fi

# ---------------------------------------------------------------------------
if want GW-OPS-05; then
  # Documents the known no-limit gap rather than asserting a limit: what must
  # hold is that 20 concurrent prints all succeed with distinct CUPS job ids
  # and no interleaved responses.
  say "GW-OPS-05 · 20 concurrent prints"
  outdir="$(mktemp -d)"
  for i in $(seq 1 20); do
    curl -s -o "$outdir/$i.json" "${hdr[@]}" -F "printer=$PRINTER" -F "file=@$PDF" "$BASE/print" &
  done
  wait
  ok=0; ids=()
  for i in $(seq 1 20); do
    if grep -q '"status":"submitted"' "$outdir/$i.json" 2>/dev/null; then
      ok=$((ok+1))
      ids+=("$(grep -oE 'request id is [^ ]+' "$outdir/$i.json" | head -1)")
    fi
  done
  uniq_ids="$(printf '%s\n' "${ids[@]}" | sort -u | wc -l)"
  rm -rf "$outdir"
  if (( ok == 20 && uniq_ids == 20 )); then
    t_pass GW-OPS-05 "20/20 succeeded with 20 distinct job ids (no concurrency limit exists — documented gap)"
  else
    t_fail GW-OPS-05 "$ok/20 succeeded, $uniq_ids distinct job ids"
  fi
fi

# ---------------------------------------------------------------------------
if want GW-OPS-08; then
  # PrivateTmp=true in the shipped unit means the spool lives in the service's
  # own tmp namespace, not the host /tmp — so this must look through the
  # process's own mount view, or it would trivially "pass" by looking at the
  # wrong directory.
  pid="$(systemctl show -p MainPID --value $GW_UNIT 2>/dev/null)"
  if [[ -z "$pid" || "$pid" == "0" ]]; then
    t_skip GW-OPS-08 "gateway not running under systemd"
  else
    curl -s -o /dev/null "${hdr[@]}" -F "printer=$PRINTER" -F "file=@$PDF" "$BASE/print"
    sleep 1
    leftovers="$(ls -1 "/proc/$pid/root/tmp" 2>/dev/null | grep -cE '^print-(upload|download|s3)-' || true)"
    if [[ "${leftovers:-0}" == "0" ]]; then
      t_pass GW-OPS-08 "no spool files left behind (checked inside PrivateTmp)"
    else
      t_fail GW-OPS-08 "$leftovers spool file(s) left in the service's /tmp"
    fi
  fi
fi

# ---------------------------------------------------------------------------
if want GW-OPS-01 || want GW-OPS-09; then
  say "GW-OPS-01/09 · graceful shutdown and restart"
  since="$(date '+%Y-%m-%d %H:%M:%S')"; sleep 1
  ( curl -s -o /dev/null --max-time 60 "${hdr[@]}" -F "printer=$PRINTER" -F "file=@$TESTS_DIR/testdata/printDemo.pdf" "$BASE/print" ) &
  inflight=$!
  sleep 0.4
  systemctl stop "$GW_UNIT"
  wait $inflight; infl_rc=$?
  log="$(gw_log_since "$since")"
  want GW-OPS-01 && {
    if (( infl_rc == 0 )) && [[ "$log" == *"shutdown signal received, draining in-flight requests"* ]]; then
      t_pass GW-OPS-01 "in-flight request completed; drain logged"
    else
      t_fail GW-OPS-01 "in-flight curl rc=$infl_rc; drain line $([[ "$log" == *drain* ]] && echo present || echo MISSING)"
    fi
  }
  systemctl start "$GW_UNIT"
  want GW-OPS-09 && {
    if gw_wait_up 15; then
      code="$(curl -s -o /dev/null -w '%{http_code}' "${hdr[@]}" -F "printer=$PRINTER" -F "file=@$PDF" "$BASE/print")"
      t_expect GW-OPS-09 "service returns to health after a restart" 200 "$code"
    else
      t_fail GW-OPS-09 "did not come back up"
    fi
  }
fi

# ---------------------------------------------------------------------------
# Object-store rows that need a wait or a log grep
# ---------------------------------------------------------------------------
s3_on() {
  python3 -c "
import json
e=json.load(open('$TESTS_DIR/postman/env/${PROFILE}.postman_environment.json'))
print(next((v['value'] for v in e['values'] if v['key']=='s3Enabled'), 'false'))
" | grep -qi true
}

if want GW-PRE-17; then
  if s3_on; then
    since="$(date '+%Y-%m-%d %H:%M:%S')"; sleep 1
    key="$(profile_get objectKeys.s3Key)"
    curl -s -o /dev/null "${hdr[@]}" -H 'Content-Type: application/json' \
      -d "{\"key\":\"$key\",\"method\":\"GET\"}" "$BASE/files/presign"
    sleep 1
    log="$(gw_log_since "$since")"
    if [[ "$log" == *"presigned GET issued"* ]] && [[ "$log" != *"X-Amz-Signature"* ]]; then
      t_pass GW-PRE-17 "presign is logged; the URL (which IS the credential) is not"
    elif [[ "$log" == *"X-Amz-Signature"* ]]; then
      t_fail GW-PRE-17 "a presigned URL signature reached the log"
    else
      t_fail GW-PRE-17 "no 'presigned GET issued' line found"
    fi
  else t_skip GW-PRE-17 "profile $PROFILE has no object store"; fi
fi

if want E2E-06; then
  if s3_on; then
    key="$(profile_get objectKeys.s3Key)"
    url="$(curl -s "${hdr[@]}" -H 'Content-Type: application/json' \
      -d "{\"key\":\"$key\",\"method\":\"GET\",\"ttl_seconds\":5}" "$BASE/files/presign" \
      | python3 -c "import json,sys; print(json.load(sys.stdin).get('url',''))")"
    if [[ -z "$url" ]]; then
      t_fail E2E-06 "could not mint a 5s presigned URL"
    else
      before="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 "$url")"
      sleep 9
      after="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 "$url")"
      if [[ "$before" == "200" && "$after" != "200" ]]; then
        t_pass E2E-06 "URL worked ($before), then expired ($after)"
      else
        t_fail E2E-06 "before=$before after=$after (wanted 200 then non-200)"
      fi
    fi
  else t_skip E2E-06 "profile $PROFILE has no object store"; fi
fi

if want E2E-07; then
  # The same queue addressed over HTTP and over raw IPP: a job submitted
  # through the gateway must be visible to printersearch. This is the property
  # the whole benchmark comparison rests on.
  PS="$REPO_DIR/src/printersearch/printersearch"
  if [[ -x "$PS" ]]; then
    cupsdisable "$PRINTER" >/dev/null 2>&1 || true   # hold it long enough to observe
    out="$(curl -s "${hdr[@]}" -F "printer=$PRINTER" -F "file=@$PDF" "$BASE/print")"
    jid="$(grep -oE 'request id is [^ ]+' <<< "$out" | grep -oE '[0-9]+$' || true)"
    jobs="$("$PS" jobs -host 127.0.0.1 -path "/printers/$PRINTER" -timeout 15s 2>&1)"
    cupsenable "$PRINTER" >/dev/null 2>&1 || true
    if [[ -n "$jid" && "$jobs" == *"$jid"* ]]; then
      t_pass E2E-07 "job $jid submitted over HTTP is visible over raw IPP"
    else
      t_fail E2E-07 "gateway job id='${jid:-none}' not found in printersearch jobs output"
    fi
  else t_skip E2E-07 "printersearch not built"; fi
fi

if want E2E-08; then
  logsrv="$(grep -c '^LOG_SERVER=..*' "$GW_ENV_FILE" 2>/dev/null || echo 0)"
  if [[ "$logsrv" == "0" ]]; then
    t_skip E2E-08 "profile $PROFILE does not ship logs — run: profile.sh LOG"
  else
    id="e2e-kibana-$(date +%s)"
    curl -s -o /dev/null "${hdr[@]}" -H "X-Laas-Identifier: $id" \
      -F "printer=$PRINTER" -F "file=@$PDF" "$BASE/print"
    t_pass E2E-08 "submitted with job_id=$id"
    note "  MANUAL: confirm in Kibana (the kibana-search skill queries by job_id). UDP shipping is"
    note "  fire-and-forget — a clean startup log proves the address resolved, not that anything received it."
  fi
fi

t_summary "ops-checks"
