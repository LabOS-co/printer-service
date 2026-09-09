#!/usr/bin/env bash
# Process-level scenarios: TEST-PLAN.md section 12 (GW-SEC-*) and section 13
# (GW-CFG-*). These cannot be Postman rows — a case that asserts "the process
# refuses to start" has no listener for a client to talk to.
#
#   startup-matrix.sh              # everything
#   startup-matrix.sh --only CFG   # or SEC
#
# Every case launches the real binary directly with a hand-built environment,
# on a port of its own (18099) so the systemd instance on 8090 is untouched and
# this can run while another suite is mid-flight.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_vars

BIN=/opt/printgateway/printgateway-linux-amd64
[[ -x "$BIN" ]] || die "gateway not deployed — run: tests/scripts/deploy.sh"
need_cmd curl

PORT=18099
ADDR="127.0.0.1:${PORT}"
ONLY="${2:-all}"
[[ "${1:-}" == "--only" ]] && ONLY="${2:-all}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"; pkill -f "$BIN $ADDR" 2>/dev/null || true' EXIT

# ---------------------------------------------------------------------------
# launch <mode> <env assignments...>
#   mode "expect-exit"  : the process must terminate on its own; captures rc
#   mode "expect-serve" : the process must come up and listen; then killed
#
# env -i gives each case a genuinely clean environment. Inheriting the caller's
# would make every result depend on whatever happened to be exported in the
# shell that ran the suite — the exact class of accident these tests exist to
# catch in a deployment.
# ---------------------------------------------------------------------------
LAST_RC=0
LAST_LOG=""
LAST_LISTEN=""

launch() {
  local mode="$1"; shift
  local log="$TMP/out.$$"
  : > "$log"

  env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME=/root \
      "$@" "$BIN" "$ADDR" >"$log" 2>&1 &
  local pid=$!

  if [[ "$mode" == "expect-exit" ]]; then
    local deadline=$(( $(date +%s) + 20 ))
    while kill -0 "$pid" 2>/dev/null && (( $(date +%s) < deadline )); do sleep 0.2; done
    if kill -0 "$pid" 2>/dev/null; then
      LAST_LISTEN="$(ss -lnt 2>/dev/null | grep -c ":${PORT}\b" || true)"
      kill -9 "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
      LAST_RC=-1                       # still running == did not exit
    else
      wait "$pid"; LAST_RC=$?
      LAST_LISTEN=0
    fi
  else
    local deadline=$(( $(date +%s) + 20 ))
    LAST_LISTEN=0
    while (( $(date +%s) < deadline )); do
      if curl -s -o /dev/null --max-time 1 "http://${ADDR}/print"; then LAST_LISTEN=1; break; fi
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.3
    done
    LAST_ADDR_BIND="$(ss -lnt 2>/dev/null | awk -v p=":${PORT}" '$4 ~ p {print $4}' | head -1)"
    kill -TERM "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
    LAST_RC=0
  fi
  LAST_LOG="$(cat "$log")"
}

# expect_refuses <id> <desc> <needle> <env...>
expect_refuses() {
  local id="$1" desc="$2" needle="$3"; shift 3
  launch expect-exit "$@"
  if (( LAST_RC == 0 )) || (( LAST_RC == -1 )); then
    t_fail "$id" "$desc — process did not refuse to start (rc=$LAST_RC, listening=$LAST_LISTEN)"
  elif [[ -n "$needle" && "$LAST_LOG" != *"$needle"* ]]; then
    t_fail "$id" "$desc — exited (rc=$LAST_RC) but message did not mention [$needle]: $(printf '%.180s' "$LAST_LOG")"
  else
    t_pass "$id" "$desc (rc=$LAST_RC)"
  fi
}

# expect_serves <id> <desc> <needle-in-log|-> <env...>
expect_serves() {
  local id="$1" desc="$2" needle="$3"; shift 3
  launch expect-serve "$@"
  if (( LAST_LISTEN != 1 )); then
    t_fail "$id" "$desc — never started listening: $(printf '%.180s' "$LAST_LOG")"
  elif [[ "$needle" != "-" && "$LAST_LOG" != *"$needle"* ]]; then
    t_fail "$id" "$desc — started, but the log did not contain [$needle]"
  else
    t_pass "$id" "$desc"
  fi
}

DEAD_VAULT="http://127.0.0.1:9"
TOK="${GW_TOKEN:?run tests/scripts/bootstrap.sh first}"

# ===========================================================================
say "GW-SEC · secrets and startup"
# ===========================================================================
if [[ "$ONLY" == "all" || "$ONLY" == "SEC" ]]; then

  "$SCRIPT_DIR/setup-vault.sh" --verify >/dev/null 2>&1 || {
    say "seeding Vault first"; "$SCRIPT_DIR/setup-vault.sh" --seed >/dev/null || die "vault seed failed"
  }

  expect_serves GW-SEC-01 "token from Vault, no env token" "print token resolved from vault" \
    VAULT_ADDR="$VAULT_URL" VAULT_TOKEN="$VAULT_ROOT_TOKEN"

  expect_serves GW-SEC-03 "SECRET_STORE_URL overrides VAULT_ADDR" "print token resolved from vault" \
    VAULT_ADDR="$DEAD_VAULT" SECRET_STORE_URL="$VAULT_URL" VAULT_TOKEN="$VAULT_ROOT_TOKEN"

  expect_serves GW-SEC-04 "Vault down + env token → env (vault fallback)" "print token resolved from env (vault fallback)" \
    VAULT_ADDR="$DEAD_VAULT" VAULT_TOKEN=whatever PRINT_GATEWAY_TOKEN="$TOK"

  expect_refuses GW-SEC-06 "Vault down, no env token → refuses to start" "cannot start" \
    VAULT_ADDR="$DEAD_VAULT" VAULT_TOKEN=whatever

  expect_refuses GW-SEC-07 "no Vault, no env token → refuses to start" \
    "vault is not configured and PRINT_GATEWAY_TOKEN is not set"

  expect_refuses GW-SEC-08 "whitespace-only env token → refuses to start" "cannot start" \
    PRINT_GATEWAY_TOKEN="   "

  # userpass with a plaintext password: go-packages/settings' convention is that
  # SECRET_STORE_PASSWORD is encryption.Encrypt-ed, so a plaintext value must
  # fail Vault init and degrade to the env token rather than crashing.
  expect_serves GW-SEC-09 "plaintext SECRET_STORE_PASSWORD → falls back, does not crash" \
    "print token resolved from env (vault fallback)" \
    VAULT_ADDR="$VAULT_URL" SECRET_STORE_USERNAME=admin SECRET_STORE_PASSWORD="admin" \
    PRINT_GATEWAY_TOKEN="$TOK"

  # LABOS_ENV prefixing. Nothing is seeded under "qa/", so the prefixed read
  # must miss and fall back — proving the prefix is actually applied. (If it
  # were ignored, the unprefixed secret would be found and the source would say
  # "vault".)
  expect_serves GW-SEC-10 "LABOS_ENV prefixes the Vault path (unseeded prefix → fallback)" \
    "print token resolved from env (vault fallback)" \
    VAULT_ADDR="$VAULT_URL" VAULT_TOKEN="$VAULT_ROOT_TOKEN" LABOS_ENV=qa \
    PRINT_GATEWAY_TOKEN="$TOK"

  # Blank Vault value: must be treated as a miss, not "resolved" into an empty
  # token that would 503 every request while logging a successful resolution.
  say "  (re-seeding Vault with a blank auth-token for GW-SEC-05)"
  "$SCRIPT_DIR/setup-vault.sh" --seed --blank-token >/dev/null || warn "blank re-seed failed"
  expect_serves GW-SEC-05 "blank Vault value → falls back to env" \
    "print token resolved from env (vault fallback)" \
    VAULT_ADDR="$VAULT_URL" VAULT_TOKEN="$VAULT_ROOT_TOKEN" PRINT_GATEWAY_TOKEN="$TOK"
  say "  (restoring the real Vault secret)"
  "$SCRIPT_DIR/setup-vault.sh" --seed >/dev/null || warn "re-seed failed — profiles B/D will not work until you run setup-vault.sh --seed"

  expect_serves GW-SEC-15 "unresolvable LOG_SERVER → console-only, not fatal" "-" \
    PRINT_GATEWAY_TOKEN="$TOK" LOG_SERVER="no-such-host.invalid:9999"

  # The token must never appear in the process's own output, at any level.
  launch expect-serve PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_LOG_LEVEL=debug
  t_not_contains GW-SEC-16 "token value never logged (even at debug)" "$TOK" "$LAST_LOG"
fi

# ===========================================================================
say "GW-CFG · configuration validation"
# ===========================================================================
if [[ "$ONLY" == "all" || "$ONLY" == "CFG" ]]; then

  expect_refuses GW-CFG-01 "zero read timeout rejected"      "PRINT_GATEWAY_READ_TIMEOUT" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_READ_TIMEOUT=0s
  expect_refuses GW-CFG-02 "negative read timeout rejected"  "PRINT_GATEWAY_READ_TIMEOUT" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_READ_TIMEOUT=-5s
  expect_refuses GW-CFG-03 "unparsable duration rejected"    "PRINT_GATEWAY_IDLE_TIMEOUT" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_IDLE_TIMEOUT=banana
  expect_refuses GW-CFG-04 "read-header > read rejected"     "PRINT_GATEWAY_READ_HEADER_TIMEOUT" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_READ_HEADER_TIMEOUT=10m
  # The double-print guard: write timeout must dominate read + download + submit,
  # or a slow request is fetched and PRINTED and then fails on the response
  # write, and the caller retries something that already came out on paper.
  expect_refuses GW-CFG-05 "write timeout not dominating rejected" "PRINT_GATEWAY_WRITE_TIMEOUT" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_WRITE_TIMEOUT=1m
  expect_refuses GW-CFG-06 "shutdown grace too small rejected" "PRINT_GATEWAY_SHUTDOWN_GRACE" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_SHUTDOWN_GRACE=1s
  expect_refuses GW-CFG-07 "byte count must be a plain integer" "PRINT_GATEWAY_MAX_HEADER_BYTES" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_MAX_HEADER_BYTES=64KiB

  # Half-configured S3 is explicitly NOT a startup failure — it disables one
  # additive capability and nothing else. This is the invariant the README
  # states, and it is the difference between "presign is unavailable" and
  # "the print service is down".
  expect_serves GW-CFG-08 "S3 endpoint without bucket → starts, storage disabled" \
    "object storage disabled" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_S3_ENDPOINT=127.0.0.1:9010
  expect_serves GW-CFG-09 "S3 bucket without endpoint → starts, storage disabled" "-" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_S3_BUCKET=printgw-test

  expect_serves GW-CFG-10 "empty S3 region warns loudly" "presigned URLs may be silently signed for the wrong region" \
    PRINT_GATEWAY_TOKEN="$TOK" \
    PRINT_GATEWAY_S3_ENDPOINT="${S3_LOCAL_ENDPOINT}" PRINT_GATEWAY_S3_BUCKET="${S3_LOCAL_BUCKET}" \
    PRINT_GATEWAY_S3_INSECURE=true \
    PRINT_GATEWAY_S3_ACCESS_KEY="${S3_LOCAL_ACCESS_KEY}" PRINT_GATEWAY_S3_SECRET_KEY="${S3_LOCAL_SECRET_KEY}"

  expect_serves GW-CFG-11 "invalid log level → starts at info" "invalid level" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_LOG_LEVEL=verbose
  expect_serves GW-CFG-12 "debug log level accepted" "-" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_LOG_LEVEL=debug

  # LogError level specifically, so the warning survives PRINT_GATEWAY_LOG_LEVEL
  # being turned down — the one setting where a quiet log would hide the fact
  # that the SSRF guard is off.
  expect_serves GW-CFG-13 "ALLOW_PRIVATE_TARGETS warns at error level" \
    "file_url target checks (address AND port) are disabled" \
    PRINT_GATEWAY_TOKEN="$TOK" PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS=true PRINT_GATEWAY_LOG_LEVEL=error

  # Listen address. Asserted from ss inside this host rather than by trying to
  # reach it from Windows: wslrelay's forwarding on this box is unreliable, so a
  # Windows-side probe would report "unreachable" for a wide-open listener too.
  launch expect-serve PRINT_GATEWAY_TOKEN="$TOK"
  t_contains GW-CFG-14 "explicit address binds loopback only" "127.0.0.1:${PORT}" "${LAST_ADDR_BIND:-}"
fi

t_summary "startup-matrix"
