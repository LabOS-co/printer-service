#!/bin/bash
set -euo pipefail

# BASE is derived from this script's own location rather than a hardcoded
# absolute path, which drifted stale the moment the working copy moved (it
# used to point at /mnt/c/printerSearch, a path that hasn't existed since
# this repo became C:\GitProjects\printer-server - see CLAUDE.md's
# "Environment gotchas" section) and even carried a case typo (HDL vs HLD)
# that only worked by accident because drvfs is case-insensitive.
BASE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

mkdir -p "$BASE/emu-spool/p1"
mkdir -p "$BASE/emu-spool/p2"
mkdir -p "$BASE/emu-spool/p3"

for n in 1 2 3; do
  port=$((9000 + n))
  cat > /etc/systemd/system/ippeve-p${n}.service <<EOF
[Unit]
Description=ippeveprinter virtual printer #${n} (perf testing)
After=network.target

[Service]
ExecStart=/usr/sbin/ippeveprinter -p ${port} -d $BASE/emu-spool/p${n} -k -v virtual-printer-${n}
Restart=always

[Install]
WantedBy=multi-user.target
EOF
done

systemctl daemon-reload
systemctl reset-failed ippeve-p1 ippeve-p2 ippeve-p3 2>/dev/null || true
systemctl restart ippeve-p1 ippeve-p2 ippeve-p3
sleep 2
systemctl is-active ippeve-p1 ippeve-p2 ippeve-p3
