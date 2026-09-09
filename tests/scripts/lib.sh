#!/usr/bin/env bash
# Shared helpers for every script in tests/scripts/.
#
# Sourced, never executed. Everything here runs INSIDE WSL — see tests/README.md
# for why the whole harness lives on that side rather than driving the gateway
# from Windows (short version: wslrelay's port forwarding does not deliver, and
# the secure profile binds loopback-only, so the only client that can reach it
# is one on the same host).
#
# Paths are derived from this file's own location, never hardcoded — the same
# rule Workstream G2 applied to src/ops/*.sh after the stale C:\printerSearch
# paths were found.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTS_DIR="$(dirname "$SCRIPT_DIR")"
REPO_DIR="$(dirname "$TESTS_DIR")"
PROFILES_JSON="$TESTS_DIR/profiles.json"
ENV_LOCAL="$TESTS_DIR/.env.local"

GW_UNIT=printgateway
GW_ENV_FILE=/etc/printgateway/printgateway.env
GW_BIN=/opt/printgw/printgateway
STATE_DIR=/var/lib/printgw-tests
REPORT_DIR="$TESTS_DIR/reports"

if [[ -t 1 ]]; then
  C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'
  C_BLU=$'\033[36m'; C_DIM=$'\033[2m'; C_OFF=$'\033[0m'
else
  C_RED=; C_GRN=; C_YEL=; C_BLU=; C_DIM=; C_OFF=
fi

say()  { printf '%s==>%s %s\n' "$C_BLU" "$C_OFF" "$*"; }
note() { printf '%s    %s%s\n' "$C_DIM" "$*" "$C_OFF"; }
warn() { printf '%sWARN%s %s\n' "$C_YEL" "$C_OFF" "$*" >&2; }
die()  { printf '%sFAIL%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; exit 1; }

need_root() {
  [[ "$(id -u)" == "0" ]] || die "run as root: wsl -d Ubuntu -u root -- bash $0 $*"
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing '$1' — run tests/scripts/bootstrap.sh first"
}

# ---------------------------------------------------------------------------
# Assertion counters, shared by ipp-suite.sh / startup-matrix.sh / ops-checks.sh
# so all three report in one format and one exit code convention.
# ---------------------------------------------------------------------------
T_PASS=0; T_FAIL=0; T_SKIP=0
declare -a T_FAILED_IDS=()

t_pass() { T_PASS=$((T_PASS+1)); printf '  %sPASS%s %-16s %s\n' "$C_GRN" "$C_OFF" "$1" "${2:-}"; }
t_fail() {
  T_FAIL=$((T_FAIL+1)); T_FAILED_IDS+=("$1")
  printf '  %sFAIL%s %-16s %s\n' "$C_RED" "$C_OFF" "$1" "${2:-}"
}
t_skip() { T_SKIP=$((T_SKIP+1)); printf '  %sSKIP%s %-16s %s\n' "$C_YEL" "$C_OFF" "$1" "${2:-}"; }

# t_expect <id> <description> <expected> <actual>
t_expect() {
  local id="$1" desc="$2" want="$3" got="$4"
  if [[ "$got" == "$want" ]]; then t_pass "$id" "$desc"
  else t_fail "$id" "$desc — expected [$want], got [$got]"; fi
}

# t_contains <id> <description> <needle> <haystack>
t_contains() {
  local id="$1" desc="$2" needle="$3" hay="$4"
  if [[ "$hay" == *"$needle"* ]]; then t_pass "$id" "$desc"
  else t_fail "$id" "$desc — expected to contain [$needle] in: $(printf '%.200s' "$hay")"; fi
}

# t_not_contains <id> <description> <needle> <haystack>
t_not_contains() {
  local id="$1" desc="$2" needle="$3" hay="$4"
  if [[ "$hay" != *"$needle"* ]]; then t_pass "$id" "$desc"
  else t_fail "$id" "$desc — must NOT contain [$needle]"; fi
}

t_summary() {
  local what="${1:-suite}"
  echo
  printf '%s: %s%d passed%s, %s%d failed%s, %s%d skipped%s\n' \
    "$what" "$C_GRN" "$T_PASS" "$C_OFF" \
    "$( ((T_FAIL)) && echo "$C_RED" || echo "$C_DIM")" "$T_FAIL" "$C_OFF" \
    "$C_YEL" "$T_SKIP" "$C_OFF"
  if ((T_FAIL)); then
    printf '  failed: %s\n' "${T_FAILED_IDS[*]}"
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# profiles.json access. python3 rather than jq: jq is not installed in this
# distro and python3 always is, so the harness has one less bootstrap step.
# ---------------------------------------------------------------------------
pj() { python3 - "$@" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
expr = sys.argv[2]
cur = data
for part in expr.split('.'):
    if part == '': continue
    if isinstance(cur, list): cur = cur[int(part)]
    else: cur = cur.get(part)
    if cur is None: break
if cur is None: print('')
elif isinstance(cur, (dict, list)): print(json.dumps(cur))
else: print(cur)
PY
}

profile_get() { pj "$PROFILES_JSON" "$1"; }

profile_names() {
  python3 -c "import json;print(' '.join(json.load(open('$PROFILES_JSON'))['profiles'].keys()))"
}

# load_vars — merge profiles.json "vars", then tests/.env.local, then the real
# environment (highest precedence), and export the lot. Deliberately in that
# order so a developer can override any default for one run without editing a
# tracked file.
load_vars() {
  local kv
  # Keys beginning with '$' are documentation, not values — profiles.json uses
  # that convention throughout ($comment, $optional). Exporting one produces
  # "invalid variable name" and, with the loop running under a shell that keeps
  # going, silently leaves every later variable unset too.
  kv="$(python3 -c "
import json
v = json.load(open('$PROFILES_JSON'))['vars']
for k, val in v.items():
    if k.startswith('\$'): continue
    print('%s=%s' % (k, val))
")"
  while IFS='=' read -r k val; do
    [[ -z "$k" ]] && continue
    # shellcheck disable=SC2163
    [[ -z "${!k:-}" ]] && export "$k=$val"
  done <<< "$kv"

  if [[ -f "$ENV_LOCAL" ]]; then
    set -a
    # shellcheck disable=SC1090
    source "$ENV_LOCAL"
    set +a
  fi
}

# resolve_s3_backend <local|internal|aws> — flattens S3_<BACKEND>_* into the
# generic S3_* names the profiles reference, so one profile definition serves
# all three backends instead of three near-identical copies.
resolve_s3_backend() {
  local b="${1:-local}" up
  up="$(echo "$b" | tr '[:lower:]' '[:upper:]')"
  local missing=()
  for f in ENDPOINT BUCKET REGION INSECURE ACCESS_KEY SECRET_KEY; do
    local src="S3_${up}_${f}"
    local val="${!src:-}"
    if [[ -z "$val" && "$f" != "INSECURE" ]]; then missing+=("$src"); fi
    export "S3_${f}=${val}"
  done
  [[ -z "${S3_INSECURE:-}" ]] && export S3_INSECURE=false
  if ((${#missing[@]})); then
    die "S3 backend '$b' is not configured — set these in $ENV_LOCAL: ${missing[*]}
     (see tests/.env.local.example; 'local' is filled in for you by setup-minio.sh)"
  fi
  export S3_BACKEND="$b"
}

# ---------------------------------------------------------------------------
# Gateway control
# ---------------------------------------------------------------------------
gw_base_url() { echo "http://${GW_ADDR:-127.0.0.1:8090}"; }

gw_wait_up() {
  local url deadline
  url="$(gw_base_url)/print"
  deadline=$(( $(date +%s) + ${1:-15} ))
  while (( $(date +%s) < deadline )); do
    # Any HTTP answer proves it is listening; 401 is the expected one for an
    # unauthenticated GET, and is what a healthy gateway returns here.
    if curl -s -o /dev/null --max-time 2 "$url"; then return 0; fi
    sleep 0.3
  done
  return 1
}

gw_wait_down() {
  local deadline; deadline=$(( $(date +%s) + ${1:-15} ))
  while (( $(date +%s) < deadline )); do
    curl -s -o /dev/null --max-time 1 "$(gw_base_url)/print" || return 0
    sleep 0.3
  done
  return 1
}

gw_log_since() { journalctl -u "$GW_UNIT" --since "$1" --no-pager -o cat 2>/dev/null; }

current_profile() { cat "$STATE_DIR/profile" 2>/dev/null || echo ""; }
current_backend() { cat "$STATE_DIR/s3_backend" 2>/dev/null || echo "local"; }

# ---------------------------------------------------------------------------
# check_print_queue [queue] — verify the queue's BACKEND is actually alive, not
# merely that the queue exists.
#
# Why this exists (found 2026-09-07): `lpstat -p vp1` reported the queue fine
# while the ippeveprinter behind it (unit ippeve-p1, 127.0.0.1:9001, created by
# src/ops/setup-emulators.sh) had been gone for days. CUPS happily ACCEPTS jobs
# for a queue whose backend is dead — `lp` returns a request id, so every
# Postman print row asserts a perfectly good 200 — and the jobs simply pile up
# unprinted. 200+ had accumulated from an earlier run, which is both a false
# green for the suite and a slow walk toward cupsd.conf's MaxJobs.
#
# Enabled/accepting is checked too: a `cupsdisable`d queue (GW-OPS-03 does that
# deliberately) left disabled would produce the same silent backlog.
# ---------------------------------------------------------------------------
check_print_queue() {
  local q="${1:-${GW_PRINTER:-vp1}}"

  lpstat -p "$q" >/dev/null 2>&1 || die "queue '$q' does not exist — run: bash $REPO_DIR/src/ops/setup-emulators.sh && bash $REPO_DIR/src/ops/setup-cups-queues-for-emulators.sh"

  local state; state="$(lpstat -p "$q" 2>/dev/null)"
  [[ "$state" == *"disabled"* ]] && die "queue '$q' is disabled — run: cupsenable $q"

  # The device URI is the only place the backend's address is recorded.
  local uri host port
  uri="$(lpstat -v "$q" 2>/dev/null | sed 's/.*: //')"
  # The \1 in each replacement is load-bearing, and was previously a literal
  # 0x01 byte (a mangled escape) rather than the two characters backslash-one.
  # With it mangled, both substitutions replace the whole URI with garbage,
  # host and port come out unusable, the </dev/tcp probe below can never
  # connect, and EVERY queue is reported dead — turning run-newman.sh's own
  # guard into a hard blocker for all profiles, the exact opposite of the
  # false-green it was added to catch.
  host="$(sed -E 's#^[a-z]+://([^:/]+).*#\1#' <<<"$uri")"
  port="$(sed -E 's#^[a-z]+://[^:/]+:([0-9]+).*#\1#' <<<"$uri")"
  [[ "$port" == "$uri" ]] && port=631

  if [[ "$uri" == ipp://* || "$uri" == ipps://* ]]; then
    if ! timeout 3 bash -c "</dev/tcp/$host/$port" >/dev/null 2>&1; then
      warn "queue '$q' points at $uri but nothing is listening there."
      warn "CUPS will still ACCEPT jobs for it and every print row will pass — the jobs just never print."
      if [[ "$host" == "127.0.0.1" && "$port" =~ ^900[1-3]$ ]]; then
        die "start the emulator: systemctl start ippeve-p${port: -1}   (or: bash $REPO_DIR/src/ops/setup-emulators.sh)"
      fi
      die "start whatever serves $uri before running the suite"
    fi
  fi

  # A backlog means an earlier run submitted into a dead backend, or the
  # backend is slower than the suite. Either way the next run's results are
  # about to be misleading, so say so loudly rather than adding to the pile.
  local pending; pending="$(lpstat -o "$q" 2>/dev/null | wc -l)"
  if (( pending > 20 )); then
    warn "queue '$q' has $pending jobs still pending — clear them first: cancel -a $q"
  fi
  return 0
}
