#!/bin/bash
set -e
mkdir -p /mnt/c/printerSearch/HDL/WSL/emu-spool/p1
mkdir -p /mnt/c/printerSearch/HDL/WSL/emu-spool/p2
mkdir -p /mnt/c/printerSearch/HDL/WSL/emu-spool/p3

for n in 1 2 3; do
  port=$((9000 + n))
  cat > /etc/systemd/system/ippeve-p${n}.service <<EOF
[Unit]
Description=ippeveprinter virtual printer #${n} (perf testing)
After=network.target

[Service]
ExecStart=/usr/sbin/ippeveprinter -p ${port} -d /mnt/c/printerSearch/HDL/WSL/emu-spool/p${n} -k -v virtual-printer-${n}
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
