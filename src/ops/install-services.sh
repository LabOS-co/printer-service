#!/bin/bash
# Installs printgateway and ippfix as version-controlled systemd units. Does not build the
# binaries or generate ippfix's printer-template.json - those are separate, per-deployment steps.
# Run as root inside the target WSL distro (distro name is "Ubuntu", not "Ubuntu-24.04").
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root (wsl -d Ubuntu -u root)" >&2
  exit 1
fi

BASE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="$BASE/../printgateway"
IPPFIX="$BASE/../ippfix"

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

  # Config file is optional; PRINT_GATEWAY_CONFIG is left commented out in printgateway.env so
  # installing the file is never the same as activating it.
  #
  # Owned by printgateway:600, not root:600 like printgateway.env above - this file is read
  # directly by the running Go process (as User=printgateway), so root-owned would be unreadable
  # to it; treat it like printgateway.env once it carries real credentials (never commit filled-in).
  #
  # Prefers the gitignored .local.json (real per-deployment secrets) over the tracked
  # secret-free .json example; never overwrites an already-installed file, so re-running this
  # installer can't clobber a live deployment's real credentials with the repo's example.
  local configfile="$BASE/../../printservice.config.local.json"
  if [ ! -f "$configfile" ]; then
    configfile="$BASE/../../printservice.config.json"
  fi
  if [ -f /etc/printgateway/printservice.config.json ]; then
    echo "note: /etc/printgateway/printservice.config.json already exists - left in place (never overwritten by this script; remove it manually first if you want the source file reinstalled)"
  elif [ -f "$configfile" ]; then
    install -m 600 -o printgateway -g printgateway "$configfile" /etc/printgateway/printservice.config.json
    echo "installed /etc/printgateway/printservice.config.json from $(basename "$configfile") (mode 600, owned by printgateway) - set PRINT_GATEWAY_CONFIG=/etc/printgateway/printservice.config.json in printgateway.env to activate it"
  else
    echo "note: no printservice.config.json or printservice.config.local.json at the repo root - the service runs on env vars/defaults only (see README's \"Configuration file\" section)"
  fi

  install -m 644 "$DEPLOY/printgateway.service" /etc/systemd/system/printgateway.service
  echo "installed printgateway.service"
}

install_ippfix() {
  local bin="$IPPFIX/ippfix"
  if [ ! -f "$bin" ]; then
    echo "warning: $bin not found - build it first (see CLAUDE.md's \"Build and run\"); skipping ippfix install" >&2
    return
  fi

  install_service_user ippfix
  mkdir -p /opt/ippfix
  install -m 755 -o ippfix -g ippfix "$bin" /opt/ippfix/ippfix
  if [ -f "$IPPFIX/printer-template.json" ]; then
    install -m 644 -o ippfix -g ippfix "$IPPFIX/printer-template.json" /opt/ippfix/printer-template.json
  else
    echo "note: no printer-template.json next to ippfix yet - generate one with 'ippfix -gen-template' (see CLAUDE.md) before starting the service"
  fi

  install -m 644 "$IPPFIX/ippfix.service" /etc/systemd/system/ippfix.service
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
