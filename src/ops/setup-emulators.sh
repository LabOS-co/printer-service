#!/bin/bash
set -euo pipefail

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
