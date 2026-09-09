#!/usr/bin/env bash
# Build the gateway for linux/amd64 and install it through the project's OWN
# ops script, so the harness tests the real deployment path.
#
#   deploy.sh              # build + install + enable
#   deploy.sh --no-build   # install an already-built binary
#
# Deliberately calls src/ops/install-services.sh rather than reimplementing it:
# that script creates the non-root printgateway user, installs the committed
# unit (ProtectSystem=strict, PrivateTmp, EnvironmentFile) and the ippfix unit.
# A harness that installed its own permissive unit could pass every scenario
# while the shipped one was broken.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_root

BUILD=1
[[ "${1:-}" == "--no-build" ]] && BUILD=0

GW_SRC="$REPO_DIR/src/printgateway"
IPPFIX_SRC="$REPO_DIR/src/ippfix"
PS_SRC="$REPO_DIR/src/printersearch"

if ((BUILD)); then
  need_cmd go
  say "building printgateway (linux/amd64)"
  # GOFLAGS/GOCACHE under /mnt/c is slow and occasionally permission-odd; keep
  # the build cache on the WSL filesystem.
  export GOCACHE=/var/cache/go-build GOMODCACHE=/var/cache/go-mod
  mkdir -p "$GOCACHE" "$GOMODCACHE"
  (cd "$GW_SRC" && GOOS=linux GOARCH=amd64 go build -o printgateway-linux-amd64 ./cmd/printgateway) \
    || die "gateway build failed"
  note "$(ls -l "$GW_SRC/printgateway-linux-amd64" | awk '{print $5" bytes  "$NF}')"

  say "building printersearch"
  (cd "$PS_SRC" && go build -o printersearch ./cmd/printersearch) || die "printersearch build failed"

  if [[ -d "$IPPFIX_SRC" ]]; then
    say "building ippfix"
    (cd "$IPPFIX_SRC" && GOOS=linux GOARCH=amd64 go build -o ippfix ./cmd/ippfix) || warn "ippfix build failed (non-fatal)"
  fi
fi

say "installing units via src/ops/install-services.sh"
bash "$REPO_DIR/src/ops/install-services.sh" || die "install-services.sh failed"

# install-services.sh deliberately does not start anything (each unit has
# per-deployment config to fill in first). The harness fills that config in via
# profile.sh, so enabling here is safe and keeps the unit up across a WSL
# restart the way docs/STATUS.md's "how to resume" section expects.
systemctl enable "$GW_UNIT" >/dev/null 2>&1 || true

if [[ ! -s "$GW_ENV_FILE" ]] || ! grep -q '^PRINT_GATEWAY_TOKEN=..*\|^VAULT_ADDR=..*' "$GW_ENV_FILE" 2>/dev/null; then
  warn "no usable $GW_ENV_FILE yet — the gateway will refuse to start until a profile is selected."
  note "run: tests/scripts/profile.sh A"
else
  systemctl restart "$GW_UNIT" || warn "restart failed — journalctl -u $GW_UNIT -n 30"
fi

say "deployed"
note "binary: /opt/printgateway/printgateway-linux-amd64"
note "unit:   $(systemctl is-enabled $GW_UNIT 2>/dev/null) / $(systemctl is-active $GW_UNIT 2>/dev/null)"
