#!/usr/bin/env bash
# The full regression: every runbook in TEST-PLAN.md section 19, in order.
#
#   run-all.sh                        # everything except paper and bench
#   run-all.sh --with-paper           # + the physically-printing rows
#   run-all.sh --bench                # + the 700-job load run
#   run-all.sh --s3-backends local,aws
#   run-all.sh --quick                # R0+R1 only (smoke + baseline contract)
#
# Runs each profile in turn, restarting the gateway between them. Keeps going
# after a failing runbook so one broken backend does not hide the state of
# everything else, and prints a per-runbook verdict at the end.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_root
load_vars

PAPER=0; BENCH=0; QUICK=0
BACKENDS="local"
while (($#)); do
  case "$1" in
    --with-paper) PAPER=1 ;;
    --bench) BENCH=1 ;;
    --quick) QUICK=1 ;;
    --s3-backends) BACKENDS="$2"; shift ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
  shift
done

declare -a RESULTS=()
run_step() {
  local label="$1"; shift
  echo
  printf '%s══ %s %s%s\n' "$C_BLU" "$label" "$(printf '═%.0s' $(seq 1 $((60 - ${#label}))))" "$C_OFF"
  if "$@"; then
    RESULTS+=("PASS  $label")
  else
    RESULTS+=("FAIL  $label")
  fi
}

newman_for() {
  local profile="$1"; shift
  "$SCRIPT_DIR/profile.sh" "$profile" "$@" || return 1
  local extra=()
  ((PAPER)) && extra+=(--with-paper)
  "$SCRIPT_DIR/run-newman.sh" "$profile" "${extra[@]}"
}

# --- prerequisites ---------------------------------------------------------
say "checking prerequisites"
systemctl is-active cups >/dev/null 2>&1 || die "cups is not running — systemctl start cups"
# Not just "does the queue exist": a queue whose backend is dead still accepts
# jobs, so every print row passes while nothing prints. See check_print_queue.
check_print_queue "${GW_PRINTER:-vp1}"
[[ -f "$TESTS_DIR/testdata/printDemo.pdf" ]] || "$SCRIPT_DIR/make-fixtures.sh" >/dev/null
"$SCRIPT_DIR/fixtures-server.sh" start >/dev/null || die "fixture server would not start"
note "cups OK · queue ${GW_PRINTER:-vp1} OK · fixtures OK"

# --- R0/R1 baseline --------------------------------------------------------
run_step "R1 · baseline contract (profile A)" newman_for A

if ((QUICK)); then
  echo; printf '%s\n' "${RESULTS[@]}"
  exit 0
fi

# --- R2 fetch / SSRF -------------------------------------------------------
run_step "R2a · permissive fetch (profile E)"          newman_for E
run_step "R2b · host allowlist (profile F)"            newman_for F
run_step "R2c · permissive + allowlist (E-allowlist)"  newman_for E-allowlist

# --- R4 object store, per backend -----------------------------------------
IFS=',' read -ra BE <<< "$BACKENDS"
for b in "${BE[@]}"; do
  [[ -z "$b" ]] && continue
  say "object-store backend: $b"
  if ! "$SCRIPT_DIR/setup-minio.sh" --backend "$b" --verify >/dev/null 2>&1; then
    if [[ "$b" == "local" ]]; then
      run_step "R4-$b · provision + seed" "$SCRIPT_DIR/setup-minio.sh" --backend local
    else
      RESULTS+=("SKIP  R4-$b · backend not configured (see tests/.env.local)")
      continue
    fi
  fi
  run_step "R4-$b · object store (profile C)" newman_for C --s3-backend "$b"
  run_step "R3-$b · limits (profile G)"       newman_for G --s3-backend "$b"
  run_step "R4-$b · fault: dead endpoint"     newman_for C-fault-endpoint --s3-backend "$b"
  run_step "R4-$b · fault: bad credentials"   newman_for C-fault-creds    --s3-backend "$b"
  run_step "R4-$b · fault: stalled store"     newman_for C-fault-slow     --s3-backend "$b"
done

# --- R5 Vault --------------------------------------------------------------
if curl -sf -o /dev/null --max-time 5 "${VAULT_URL}/v1/sys/health" 2>/dev/null; then
  run_step "R5a · Vault-sourced token (profile B)" newman_for B
  primary="${BE[0]:-local}"
  if "$SCRIPT_DIR/setup-minio.sh" --backend "$primary" --verify >/dev/null 2>&1; then
    run_step "R5b · Vault token + Vault S3 creds (profile D)" newman_for D --s3-backend "$primary"
  else
    RESULTS+=("SKIP  R5b · profile D needs a working object store")
  fi
else
  RESULTS+=("SKIP  R5 · Vault unreachable at $VAULT_URL")
fi

# --- R6 startup matrix -----------------------------------------------------
run_step "R6 · startup matrix (GW-SEC / GW-CFG)" "$SCRIPT_DIR/startup-matrix.sh"

# --- R7 IPP / CLI ----------------------------------------------------------
ipp_args=()
((PAPER)) && ipp_args+=(--with-paper)
((BENCH)) && ipp_args+=(--bench)
run_step "R7 · printersearch + ippfix" "$SCRIPT_DIR/ipp-suite.sh" "${ipp_args[@]}"

# --- R10 operational -------------------------------------------------------
# Back to a healthy profile first: the fault profiles above leave the gateway
# pointed at a dead store, and every ops row needs a working one.
primary="${BE[0]:-local}"
if "$SCRIPT_DIR/setup-minio.sh" --backend "$primary" --verify >/dev/null 2>&1; then
  "$SCRIPT_DIR/profile.sh" C --s3-backend "$primary" >/dev/null
else
  "$SCRIPT_DIR/profile.sh" A >/dev/null
fi
run_step "R10 · operational behaviour" "$SCRIPT_DIR/ops-checks.sh"

# --- verdict ---------------------------------------------------------------
echo
printf '%s══ summary %s%s\n' "$C_BLU" "$(printf '═%.0s' $(seq 1 51))" "$C_OFF"
fails=0
for r in "${RESULTS[@]}"; do
  case "$r" in
    PASS*) printf '  %s%s%s\n' "$C_GRN" "$r" "$C_OFF" ;;
    SKIP*) printf '  %s%s%s\n' "$C_YEL" "$r" "$C_OFF" ;;
    *)     printf '  %s%s%s\n' "$C_RED" "$r" "$C_OFF"; fails=$((fails+1)) ;;
  esac
done
echo
note "newman reports: $REPORT_DIR"
((PAPER)) || note "paper rows were skipped — rerun with --with-paper and confirm at the printer"
((BENCH)) || note "bench rows were skipped — rerun with --bench (needs the 15-queue fleet)"

exit $(( fails > 0 ))
