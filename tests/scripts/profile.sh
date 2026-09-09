#!/usr/bin/env bash
# Switch the running Print Gateway to a test profile (TEST-PLAN.md section 4).
#
#   profile.sh A                          # baseline
#   profile.sh C --s3-backend aws         # object storage, real AWS S3
#   profile.sh --list
#   profile.sh --show                     # what is running right now
#
# Reuses the project's OWN committed unit (src/printgateway/printgateway.service,
# installed by src/ops/install-services.sh) rather than inventing a parallel
# test unit. That is deliberate: it means every profile switch exercises the
# real deployment path — the non-root printgateway user, ProtectSystem=strict,
# PrivateTmp, EnvironmentFile — instead of a permissive stand-in that could pass
# while the shipped unit was broken.
#
# The only thing written here is /etc/printgateway/printgateway.env (root:root,
# 0600), which is exactly where that unit already expects every secret to live.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

usage() {
  cat <<EOF
usage: profile.sh <PROFILE> [--s3-backend local|internal|aws] [--no-wait]
       profile.sh --list | --show | --stop

profiles:
$(python3 -c "
import json
p = json.load(open('$PROFILES_JSON'))['profiles']
for k, v in p.items():
    print('  %-18s %s' % (k, v['label']))
")
EOF
}

S3_BACKEND_ARG=""
PROFILE=""
WAIT=1

while (($#)); do
  case "$1" in
    --list) usage; exit 0 ;;
    --show)
      echo "profile:    $(current_profile)"
      echo "s3 backend: $(current_backend)"
      echo "unit:       $(systemctl is-active $GW_UNIT 2>/dev/null)"
      echo "listening:  $(ss -lntp 2>/dev/null | grep -E ':8090' || echo 'nothing on 8090')"
      exit 0 ;;
    --stop) need_root; systemctl stop "$GW_UNIT"; say "stopped"; exit 0 ;;
    --s3-backend) S3_BACKEND_ARG="$2"; shift ;;
    --no-wait) WAIT=0 ;;
    -h|--help) usage; exit 0 ;;
    -*) die "unknown option: $1" ;;
    *) PROFILE="$1" ;;
  esac
  shift
done

[[ -n "$PROFILE" ]] || { usage; exit 1; }
need_root
need_cmd curl

load_vars

label="$(profile_get "profiles.$PROFILE.label")"
[[ -n "$label" ]] || die "no such profile: $PROFILE (try --list)"

requires="$(profile_get "profiles.$PROFILE.requires")"
requires="${requires//[\[\]\"]/}"; requires="${requires//,/ }"

# ---------------------------------------------------------------------------
# Backend selection. A profile that needs object storage gets its generic
# S3_* variables flattened from the chosen backend's S3_<BACKEND>_* set, so one
# profile definition covers local MinIO, the internal MinIO and real AWS.
# ---------------------------------------------------------------------------
backend="${S3_BACKEND_ARG:-$(current_backend)}"
if [[ " $requires " == *" s3 "* ]] || [[ "$PROFILE" == C-fault-* ]]; then
  resolve_s3_backend "$backend"
  note "s3 backend: $backend ($S3_ENDPOINT, bucket $S3_BUCKET, region ${S3_REGION:-<unset>}, insecure=$S3_INSECURE)"
else
  # Still resolve when possible: the C-fault-slow profile references
  # ${S3_BUCKET} even though it does not require a reachable store.
  resolve_s3_backend "$backend" 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Prerequisite checks. Each failure names the exact command that fixes it —
# a profile switch that half-works produces a run full of confusing 503s
# forty scenarios later, which is much harder to diagnose than failing here.
# ---------------------------------------------------------------------------
for r in $requires; do
  case "$r" in
    vault)
      curl -sf -o /dev/null --max-time 5 "${VAULT_URL}/v1/sys/health" \
        || die "Vault unreachable at $VAULT_URL — is the local-vault container up? (docker ps | grep local-vault)"
      if ! "$SCRIPT_DIR/setup-vault.sh" --verify >/dev/null 2>&1; then
        say "seeding Vault (secret not present yet)"
        "$SCRIPT_DIR/setup-vault.sh" --seed || die "vault seed failed"
      fi
      ;;
    s3)
      if [[ "$PROFILE" != C-fault-* ]]; then
        s3_scheme=https; [[ "${S3_INSECURE:-false}" == "true" ]] && s3_scheme=http
        curl -sk -o /dev/null --max-time 8 "${s3_scheme}://${S3_ENDPOINT}/minio/health/live" \
          || curl -sk -o /dev/null --max-time 8 "${s3_scheme}://${S3_ENDPOINT}/" \
          || die "object store unreachable at ${S3_ENDPOINT} — run: tests/scripts/setup-minio.sh --backend $backend"
      fi
      ;;
    fixtures)
      curl -sf -o /dev/null --max-time 3 "http://${FIXTURE_HOST}:${FIXTURE_PORT}/health" \
        || die "fixture server not running — run: tests/scripts/fixtures-server.sh start"
      ;;
  esac
done

# ---------------------------------------------------------------------------
# Render the env file
# ---------------------------------------------------------------------------
[[ -x "$GW_BIN" || -x /opt/printgateway/printgateway-linux-amd64 ]] \
  || die "gateway not deployed — run: tests/scripts/deploy.sh"

mkdir -p /etc/printgateway "$STATE_DIR"

env_body="$(python3 - "$PROFILES_JSON" "$PROFILE" <<'PY'
import json, os, sys, re
profiles = json.load(open(sys.argv[1]))
prof = profiles['profiles'][sys.argv[2]]
missing = []

def sub(v):
    def rep(m):
        name = m.group(1)
        val = os.environ.get(name, '')
        if val == '' and name not in ('LABOS_ENV_VALUE', 'S3_REGION'):
            missing.append(name)
        return val
    return re.sub(r'\$\{([A-Z0-9_]+)\}', rep, str(v))

lines = [
    '# GENERATED by tests/scripts/profile.sh — do not edit by hand.',
    '# profile: %s (%s)' % (sys.argv[2], prof['label']),
    '# %s' % prof['description'],
    '',
]
for k, v in prof['gateway'].items():
    if k == 'GW_ADDR':
        continue  # consumed by the harness, not by the gateway
    lines.append('%s=%s' % (k, sub(v)))

if missing:
    sys.stderr.write('UNRESOLVED:' + ','.join(sorted(set(missing))) + '\n')
    sys.exit(3)
print('\n'.join(lines))
PY
)" || die "profile $PROFILE has unresolved variables — fill them in $ENV_LOCAL (see tests/.env.local.example)"

umask 077
printf '%s\n' "$env_body" > "$GW_ENV_FILE"
chown root:root "$GW_ENV_FILE"; chmod 600 "$GW_ENV_FILE"

export GW_ADDR="$(profile_get "profiles.$PROFILE.gateway.GW_ADDR")"
GW_ADDR="${GW_ADDR/\$\{GW_ADDR\}/127.0.0.1:8090}"
[[ "$GW_ADDR" == *'${'* ]] && GW_ADDR="127.0.0.1:8090"

echo "$PROFILE"  > "$STATE_DIR/profile"
echo "$backend"  > "$STATE_DIR/s3_backend"
echo "$GW_ADDR"  > "$STATE_DIR/addr"

# ---------------------------------------------------------------------------
# Restart and confirm
# ---------------------------------------------------------------------------
say "profile $PROFILE ($label)"
started_at="$(date '+%Y-%m-%d %H:%M:%S')"
systemctl restart "$GW_UNIT" || die "systemctl restart $GW_UNIT failed — journalctl -u $GW_UNIT -n 40"

if ((WAIT)); then
  if ! gw_wait_up 20; then
    echo "--- last 30 log lines ---" >&2
    journalctl -u "$GW_UNIT" -n 30 --no-pager >&2
    die "gateway did not come up on $GW_ADDR"
  fi
fi

# The startup log names which source produced the print token. Surfacing it on
# every profile switch is what turns "the Vault profile passed" into "the Vault
# profile passed BECAUSE Vault answered" — without it, a silent fallback to the
# environment token would make profile B indistinguishable from profile A.
sleep 0.4
src_line="$(gw_log_since "$started_at" | grep -iE 'print token (resolved )?from|token source' | tail -1)"
[[ -n "$src_line" ]] && note "token source: $src_line"

case "$PROFILE" in
  B|D)
    if [[ "$src_line" != *vault* ]] || [[ "$src_line" == *fallback* ]]; then
      warn "profile $PROFILE expects the token to come from Vault, but the startup log says: ${src_line:-<nothing>}"
      warn "the Vault-precedence scenarios (GW-SEC-02a/b) will fail — check: $SCRIPT_DIR/setup-vault.sh --verify"
    fi
    ;;
esac

note "listening: $(ss -lntp 2>/dev/null | grep ':8090' | head -1)"
say "ready — run: $SCRIPT_DIR/run-newman.sh $PROFILE"
