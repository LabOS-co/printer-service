#!/usr/bin/env bash
# Stand up the local MinIO backend and seed the bucket that every s3_key /
# presign scenario expects — or seed an existing internal-MinIO / AWS bucket.
#
#   setup-minio.sh                          # local: install unit, start, seed
#   setup-minio.sh --backend internal       # seed only (server already exists)
#   setup-minio.sh --backend aws            # seed only (real AWS S3)
#   setup-minio.sh --verify [--backend X]   # is it reachable and seeded?
#   setup-minio.sh --stop                   # local only
#
# The local server runs as a systemd unit, not a background process: CLAUDE.md's
# own lesson is that `bash -c "... &"` inside WSL does not survive the invoking
# command returning, and the whole point of a test backend is that it is still
# there after a reboot.
#
# Port 9010, not the MinIO default 9000 — portainer already occupies 9000 on
# this box, and a test fixture silently talking to portainer would be a
# genuinely confusing failure.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_vars

BACKEND=local
ACTION=setup

while (($#)); do
  case "$1" in
    --backend) BACKEND="$2"; shift ;;
    --verify) ACTION=verify ;;
    --stop) ACTION=stop ;;
    --seed-only) ACTION=seed ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
  shift
done

resolve_s3_backend "$BACKEND"
SCHEME=https; [[ "${S3_INSECURE:-false}" == "true" ]] && SCHEME=http
ALIAS="pgw-$BACKEND"

# mc_alias configures the alias in ~/.mc/config.json via `mc alias set`, NOT via
# the MC_HOST_<alias> environment variable.
#
# MC_HOST_ is the more obvious choice and it is a trap: the alias name becomes
# part of the variable name, so "pgw-local" produces MC_HOST_pgw-local, which
# bash refuses to export ("not a valid identifier"). The variable is then never
# set, and mc — finding no alias by that name — silently reinterprets
# "pgw-local/printgw-test/..." as a LOCAL FILESYSTEM PATH. Every upload
# "succeeds", every stat "succeeds", and 5 MB of fixtures land in a directory
# named pgw-local/ next to wherever the script ran, while the bucket is never
# created at all. Verified: the first run of this script did exactly that, and
# --verify reported the backend fully seeded.
#
# `mc alias set` has no identifier constraint and fails loudly on bad
# credentials or an unreachable endpoint, which is the behaviour needed here.
mc_alias() {
  need_cmd mc
  mc --quiet alias set "$ALIAS" "${SCHEME}://${S3_ENDPOINT}" "$S3_ACCESS_KEY" "$S3_SECRET_KEY" \
     ${S3_REGION:+--api S3v4} >/dev/null 2>&1 \
    || die "could not register mc alias for ${SCHEME}://${S3_ENDPOINT} — endpoint unreachable, or credentials rejected"
  # Belt and braces: prove mc really resolves the alias remotely rather than
  # falling back to a same-named local directory.
  mc --quiet ls "${ALIAS}" >/dev/null 2>&1 \
    || die "mc alias '$ALIAS' does not resolve — refusing to continue (a local-path fallback would silently do nothing)"
  [[ ! -d "$ALIAS" ]] \
    || die "a local directory named '$ALIAS' exists in $(pwd) — mc would prefer it over the alias; remove it and re-run"
}

# ---------------------------------------------------------------------------
# Local server lifecycle
# ---------------------------------------------------------------------------
MINIO_DATA=/var/lib/minio-printgw
MINIO_UNIT=minio-printgw

install_local_server() {
  need_root
  need_cmd minio
  mkdir -p "$MINIO_DATA"

  cat > /etc/systemd/system/${MINIO_UNIT}.service <<EOF
[Unit]
Description=MinIO for LAB-16894 print-gateway system tests
After=network.target

[Service]
Type=simple
Environment=MINIO_ROOT_USER=${S3_ACCESS_KEY}
Environment=MINIO_ROOT_PASSWORD=${S3_SECRET_KEY}
# --address pins the S3 API port; the console gets its own so it cannot
# wander onto a port another service already holds.
ExecStart=/usr/local/bin/minio server ${MINIO_DATA} --address ${S3_ENDPOINT} --console-address 127.0.0.1:9011
Restart=always
RestartSec=2s

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable --now ${MINIO_UNIT} >/dev/null 2>&1
  say "minio unit ${MINIO_UNIT} $(systemctl is-active ${MINIO_UNIT})"

  local deadline=$(( $(date +%s) + 30 ))
  while (( $(date +%s) < deadline )); do
    curl -sf -o /dev/null --max-time 2 "${SCHEME}://${S3_ENDPOINT}/minio/health/live" && return 0
    sleep 0.5
  done
  journalctl -u ${MINIO_UNIT} -n 20 --no-pager >&2
  die "minio did not become healthy on ${S3_ENDPOINT}"
}

# ---------------------------------------------------------------------------
# Seeding. The key list lives in profiles.json so the seeder and the collection
# can never disagree about what is supposed to be in the bucket.
# ---------------------------------------------------------------------------
seed_bucket() {
  local td="$TESTS_DIR/testdata"
  [[ -f "$td/printDemo.pdf" ]] || die "fixtures missing — run tests/scripts/make-fixtures.sh"
  mc_alias

  local mcflags=(--quiet)

  if ! mc "${mcflags[@]}" ls "${ALIAS}/${S3_BUCKET}" >/dev/null 2>&1; then
    say "creating bucket ${S3_BUCKET}"
    mc "${mcflags[@]}" mb --ignore-existing "${ALIAS}/${S3_BUCKET}" \
      || die "could not create bucket ${S3_BUCKET} on ${S3_ENDPOINT} (do the credentials allow it? for AWS the bucket usually must pre-exist)"
  fi

  say "seeding ${S3_BUCKET} (backend: $BACKEND)"
  local key src
  while IFS='=' read -r var key; do
    [[ -z "$key" ]] && continue
    case "$var" in
      s3Key)      src="$td/printDemo.pdf" ;;
      s3SmallKey) src="$td/small.pdf" ;;
      s3LargeKey) src="$td/large.bin" ;;
      s3TextKey)  src="$td/not-a-pdf.txt" ;;
      s3EmptyKey) src="$td/empty.pdf" ;;
      # small.pdf, not printDemo.pdf: this scenario is about the KEY (spaces and
      # non-ASCII), not the payload, and a 2.5 MB body would additionally trip
      # profile G's tightened S3 size cap and fail for the wrong reason.
      s3SpaceKey) src="$td/small.pdf" ;;
      # s3MissingKey is deliberately never uploaded — GW-S3-04 needs a real miss.
      s3MissingKey)
        mc "${mcflags[@]}" rm "${ALIAS}/${S3_BUCKET}/${key}" >/dev/null 2>&1 || true
        note "absent (on purpose): $key"
        continue ;;
      *) continue ;;
    esac
    mc "${mcflags[@]}" cp "$src" "${ALIAS}/${S3_BUCKET}/${key}" >/dev/null \
      || die "upload failed: $key"
    note "uploaded: $key  ($(stat -c%s "$src") bytes)"
  done < <(python3 -c "
import json
for k, v in json.load(open('$PROFILES_JSON'))['objectKeys'].items():
    print('%s=%s' % (k, v))
")

  # E2E-01 writes here; clear it so a rerun proves the upload happened again
  # rather than passing on a leftover from the previous run.
  mc "${mcflags[@]}" rm "${ALIAS}/${S3_BUCKET}/test/e2e-upload.pdf" >/dev/null 2>&1 || true
}

verify() {
  curl -sk -o /dev/null --max-time 8 "${SCHEME}://${S3_ENDPOINT}/minio/health/live" \
    || curl -sk -o /dev/null --max-time 8 "${SCHEME}://${S3_ENDPOINT}/" \
    || die "not reachable: ${SCHEME}://${S3_ENDPOINT}"
  mc_alias

  local want missing=0
  while read -r want; do
    [[ -z "$want" ]] && continue
    if mc --quiet stat "${ALIAS}/${S3_BUCKET}/${want}" >/dev/null 2>&1; then
      note "present: $want"
    else
      warn "MISSING: $want"; missing=1
    fi
  done < <(python3 -c "
import json
d = json.load(open('$PROFILES_JSON'))['objectKeys']
for k, v in d.items():
    if k != 's3MissingKey': print(v)
")

  # GW-S3-04 needs a genuine miss, so this key must NOT be there. Asserted
  # rather than assumed: a leftover from an earlier experiment would turn that
  # scenario's 404 into a 200 and look like a service bug.
  local missing_key; missing_key="$(profile_get objectKeys.s3MissingKey)"
  if mc --quiet stat "${ALIAS}/${S3_BUCKET}/${missing_key}" >/dev/null 2>&1; then
    die "'${missing_key}' EXISTS in ${S3_BUCKET} but GW-S3-04 needs it absent — remove it"
  fi
  note "absent as required: $missing_key"

  ((missing)) && die "bucket ${S3_BUCKET} is not fully seeded — run: setup-minio.sh --backend $BACKEND"
  say "backend '$BACKEND' reachable and seeded (${S3_ENDPOINT}/${S3_BUCKET})"
}

case "$ACTION" in
  stop)
    [[ "$BACKEND" == "local" ]] || die "--stop only applies to the local backend"
    need_root; systemctl stop ${MINIO_UNIT}; say "stopped ${MINIO_UNIT}" ;;
  verify) verify ;;
  seed)   seed_bucket ;;
  setup)
    [[ "$BACKEND" == "local" ]] && install_local_server
    seed_bucket
    verify ;;
esac
