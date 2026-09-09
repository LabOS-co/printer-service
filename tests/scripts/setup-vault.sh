#!/usr/bin/env bash
# Seed / verify / remove the Print Gateway's secrets in the local Vault.
#
#   setup-vault.sh --seed      # write the four keys
#   setup-vault.sh --verify    # exit 0 if the print token is present and non-empty
#   setup-vault.sh --show      # print what is there (values masked)
#   setup-vault.sh --unseed    # delete the secret again
#   setup-vault.sh --seed --blank-token   # GW-SEC-05: present-but-empty value
#
# Path and keys mirror gSecretManager's own convention exactly — that is the
# whole point of the README's "Secrets (Vault)" section: a Vault-backed
# deployment needs no new path convention on either side.
#
#   mount   kv-v2            (NOT the dev-mode default "secret" — go-packages/
#                             secret_store hardcodes kv-v2/data as its root)
#   path    [<LABOS_ENV>/]config/print_gateway
#   keys    auth-token · log-server · s3-access-key · s3-secret-key
#
# Uses the HTTP API via curl rather than the vault CLI, which is not installed
# in this distro and would be one more bootstrap dependency for four writes.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_cmd curl
load_vars

ACTION=""
BLANK_TOKEN=0
BACKEND="$(current_backend)"

while (($#)); do
  case "$1" in
    --seed|--verify|--show|--unseed) ACTION="${1#--}" ;;
    --blank-token) BLANK_TOKEN=1 ;;
    --backend) BACKEND="$2"; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
  shift
done
[[ -n "$ACTION" ]] || { sed -n '2,20p' "$0"; exit 1; }

# LABOS_ENV prefixing: the gateway builds "<env>/config/print_gateway" and only
# adds the prefix when the variable is set, so the seeder must apply the exact
# same rule or profile B silently reads a path nobody wrote to.
prefix="${LABOS_ENV_VALUE:-}"
prefix="${prefix#/}"; prefix="${prefix%/}"
if [[ -n "$prefix" ]]; then
  SECRET_PATH="${prefix}/${VAULT_SECRET_PATH}"
else
  SECRET_PATH="${VAULT_SECRET_PATH}"
fi
DATA_URL="${VAULT_URL}/v1/${VAULT_MOUNT}/data/${SECRET_PATH}"
META_URL="${VAULT_URL}/v1/${VAULT_MOUNT}/metadata/${SECRET_PATH}"

vault_curl() { curl -s -H "X-Vault-Token: ${VAULT_ROOT_TOKEN}" "$@"; }

read_key() {
  vault_curl "$DATA_URL" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print(''); raise SystemExit
print((d.get('data') or {}).get('data', {}).get('$1', '') or '')
"
}

case "$ACTION" in
  verify)
    v="$(read_key auth-token)"
    [[ -n "$v" ]] || exit 1
    exit 0
    ;;

  show)
    say "vault ${VAULT_MOUNT}/${SECRET_PATH}"
    for k in auth-token log-server s3-access-key s3-secret-key; do
      v="$(read_key "$k")"
      if [[ -z "$v" ]]; then
        printf '  %-16s %s(absent)%s\n' "$k" "$C_DIM" "$C_OFF"
      elif [[ "$k" == log-server ]]; then
        printf '  %-16s %s\n' "$k" "$v"
      else
        printf '  %-16s %s… (%d chars)\n' "$k" "${v:0:4}" "${#v}"
      fi
    done
    ;;

  unseed)
    vault_curl -X DELETE "$META_URL" >/dev/null
    say "removed ${VAULT_MOUNT}/${SECRET_PATH}"
    ;;

  seed)
    [[ -n "${GW_VAULT_TOKEN:-}" ]] || die "GW_VAULT_TOKEN is not set — run tests/scripts/bootstrap.sh"
    curl -sf -o /dev/null --max-time 5 "${VAULT_URL}/v1/sys/health" \
      || die "Vault unreachable at $VAULT_URL (docker ps | grep local-vault)"

    token_value="$GW_VAULT_TOKEN"
    ((BLANK_TOKEN)) && token_value=""

    # S3 credentials are seeded too, for profile D (GW-SEC-11): the gateway
    # reads s3-access-key/s3-secret-key from the SAME path as the print token,
    # different keys, and profile D deliberately supplies neither in the
    # environment — so a passing GW-S3-02 there proves the Vault credential path.
    resolve_s3_backend "$BACKEND" 2>/dev/null || true

    payload="$(TOKEN_VALUE="$token_value" \
               LOG_SERVER_ADDR="${LOG_SERVER_ADDR:-}" \
               S3_ACCESS_KEY="${S3_ACCESS_KEY:-}" \
               S3_SECRET_KEY="${S3_SECRET_KEY:-}" \
               python3 -c "
import json, os
print(json.dumps({'data': {
    'auth-token':    os.environ['TOKEN_VALUE'],
    'log-server':    os.environ.get('LOG_SERVER_ADDR', ''),
    's3-access-key': os.environ.get('S3_ACCESS_KEY', ''),
    's3-secret-key': os.environ.get('S3_SECRET_KEY', ''),
}}))
")"

    resp="$(vault_curl -X POST -H 'Content-Type: application/json' -d "$payload" "$DATA_URL")"
    # A successful kv-v2 write returns a "data" block with a version number; a
    # failure returns a non-empty "errors" array. Checking for the success shape
    # rather than the absence of errors, so an unexpected response body fails
    # loudly instead of being mistaken for a write that worked.
    echo "$resp" | python3 -c "
import json, sys
d = json.load(sys.stdin)
if d.get('errors'):
    sys.stderr.write('vault: ' + '; '.join(d['errors']) + '\n'); raise SystemExit(1)
if not (d.get('data') or {}).get('version'):
    sys.stderr.write('vault: unexpected write response: ' + json.dumps(d)[:200] + '\n'); raise SystemExit(1)
" || die "vault write to ${VAULT_MOUNT}/${SECRET_PATH} failed"

    say "seeded ${VAULT_MOUNT}/${SECRET_PATH}"
    if ((BLANK_TOKEN)); then
      note "auth-token written BLANK on purpose (GW-SEC-05: a present-but-empty value must fall back to the environment, not 'succeed' into an empty token that would 503 every request)"
    else
      note "auth-token   ${GW_VAULT_TOKEN:0:4}… (${#GW_VAULT_TOKEN} chars)"
    fi
    note "log-server   ${LOG_SERVER_ADDR:-<empty>}"
    note "s3 creds     ${S3_ACCESS_KEY:+seeded from backend '$BACKEND'}${S3_ACCESS_KEY:-<empty — profile D will fall back to env>}"
    ;;
esac
