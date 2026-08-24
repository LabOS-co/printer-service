#!/bin/bash
set -e
BASE=/mnt/c/printerSearch/HDL/WSL

# name:port:manufacturer:model:supports_pdf(1/0)
PRINTERS=(
  "hp-laserjet:9101:HP:LaserJet Pro M404:1"
  "canon-ir:9102:Canon:imageRUNNER ADVANCE:1"
  "xerox-versalink:9103:Xerox:VersaLink C405:1"
  "epson-wf:9104:Epson:WorkForce Pro:1"
  "kyocera-ecosys:9105:Kyocera:ECOSYS M2540:1"
  "ricoh-mp:9106:Ricoh:MP C3004:1"
  "lexmark-mx:9107:Lexmark:MX521:1"
  "konica-bizhub:9108:Konica Minolta:bizhub C3350:1"
  "sharp-mx:9109:Sharp:MX-3070:1"
  "dell-smart:9110:Dell:Smart Printer S2815:1"
  "brother-hl:9111:Brother:HL-L2350DW:0"
  "samsung-proxpress:9112:Samsung:ProXpress M4020:0"
  "zebra-zt:9113:Zebra:ZT411:0"
  "star-tsp:9114:Star:TSP143:0"
  "oki-b432:9115:OKI:B432:0"
)

mkdir -p "$BASE/emu15-spool"

for entry in "${PRINTERS[@]}"; do
  IFS=':' read -r name port mfg model pdf <<< "$entry"
  mkdir -p "$BASE/emu15-spool/$name"
  if [ "$pdf" = "1" ]; then
    formats="application/pdf,application/octet-stream,image/pwg-raster,image/urf"
  else
    formats="application/octet-stream,image/pwg-raster,image/urf"
  fi
  cat > /etc/systemd/system/ippeve-${name}.service <<EOF
[Unit]
Description=ippeveprinter ${mfg} ${model} (perf testing, pdf=${pdf})
After=network.target

[Service]
ExecStart=/usr/sbin/ippeveprinter -p ${port} -d "$BASE/emu15-spool/$name" -M "${mfg}" -m "${model}" -f "${formats}" -k "${name}"
Restart=always

[Install]
WantedBy=multi-user.target
EOF
done

systemctl daemon-reload
names=()
for entry in "${PRINTERS[@]}"; do
  IFS=':' read -r name _ <<< "$entry"
  names+=("ippeve-${name}")
done
systemctl reset-failed "${names[@]}" 2>/dev/null || true
systemctl enable --now "${names[@]}"
sleep 2
systemctl is-active "${names[@]}"
