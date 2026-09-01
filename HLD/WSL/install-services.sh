#!/bin/bash
# G1: installs the two long-running services this project depends on
# (printgateway, ippfix) as version-controlled systemd units, so they
# survive a WSL distro reinstall the way CLAUDE.md's own lesson says any
# long-running WSL process must (`wsl.exe ... bash -c "... &"` does not
# keep a background process alive past the invoking command returning, and
# socket/path-activated units have separately caused real outages here -
# see CLAUDE.md's "Environment gotchas").
#
# Run as root inside the target WSL distro: wsl -d Ubuntu -u root
# (CLAUDE.md's own memory notes the distro is actually named "Ubuntu", not
# "Ubuntu-24.04" as the original setup scripts assumed).
#
# This script installs units and binaries; it does NOT build the binaries
# (see CLAUDE.md's "Build and run" section) or generate ippfix's required
# printer-template.json (see CLAUDE.md's ippfix section) - both are
# per-deployment steps with their own explicit commands, not safe to run
# unattended from here.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root (wsl -d Ubuntu -u root)" >&2
  exit 1
fi

# BASE is this script's own directory (HLD/WSL) - see setup-emulators.sh's
# comment for why that beats a hardcoded absolute path.
BASE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="$BASE/../deploy"

install_service_user() {
  local user=$1
  if ! id "$user" >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "$user"
    echo "created system user: $user"
  fi
}

install_printgateway() {
  local bin="$DEPLOY/printgateway-linux-amd64"
  if [ ! -f "$bin" ]; then
    echo "warning: $bin not found - build it first (see CLAUDE.md's \"Build and run\"); skipping printgateway install" >&2
    return
  fi

  install_service_user printgateway
  mkdir -p /opt/printgateway /etc/printgateway
  install -m 755 -o printgateway -g printgateway "$bin" /opt/printgateway/printgateway-linux-amd64

  if [ ! -f /etc/printgateway/printgateway.env ]; then
    install -m 600 -o root -g root "$DEPLOY/printgateway.env.example" /etc/printgateway/printgateway.env
    echo "wrote a blank /etc/printgateway/printgateway.env - fill in PRINT_GATEWAY_TOKEN (or Vault vars) before starting the service"
  fi

  install -m 644 "$DEPLOY/printgateway.service" /etc/systemd/system/printgateway.service
  echo "installed printgateway.service"
}

install_ippfix() {
  local bin="$BASE/ippfix/ippfix"
  if [ ! -f "$bin" ]; then
    echo "warning: $bin not found - build it first (see CLAUDE.md's \"Build and run\"); skipping ippfix install" >&2
    return
  fi

  install_service_user ippfix
  mkdir -p /opt/ippfix
  install -m 755 -o ippfix -g ippfix "$bin" /opt/ippfix/ippfix
  if [ -f "$BASE/ippfix/printer-template.json" ]; then
    install -m 644 -o ippfix -g ippfix "$BASE/ippfix/printer-template.json" /opt/ippfix/printer-template.json
  else
    echo "note: no printer-template.json next to ippfix yet - generate one with 'ippfix -gen-template' (see CLAUDE.md) before starting the service"
  fi

  install -m 644 "$BASE/ippfix/ippfix.service" /etc/systemd/system/ippfix.service
  echo "installed ippfix.service (edit its ExecStart -target/-listen for this deployment before starting)"
}

install_printgateway
install_ippfix

systemctl daemon-reload
echo ""
echo "units installed. Review /etc/printgateway/printgateway.env and ippfix.service's ExecStart, then:"
echo "  systemctl enable --now printgateway"
echo "  systemctl enable --now ippfix"
echo "(deliberately not started automatically by this script - each has per-deployment config to fill in first)"
