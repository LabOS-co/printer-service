#!/bin/bash
set -e

PRINTERS=(
  "hp-laserjet:9101:1"
  "canon-ir:9102:1"
  "xerox-versalink:9103:1"
  "epson-wf:9104:1"
  "kyocera-ecosys:9105:1"
  "ricoh-mp:9106:1"
  "lexmark-mx:9107:1"
  "konica-bizhub:9108:1"
  "sharp-mx:9109:1"
  "dell-smart:9110:1"
  "brother-hl:9111:0"
  "samsung-proxpress:9112:0"
  "zebra-zt:9113:0"
  "star-tsp:9114:0"
  "oki-b432:9115:0"
)

for entry in "${PRINTERS[@]}"; do
  IFS=':' read -r name port pdf <<< "$entry"
  qname="q-${name}"
  lpadmin -x "$qname" 2>/dev/null || true
  lpadmin -p "$qname" -E -v ipp://127.0.0.1:${port}/ipp/print -m everywhere
  cupsenable "$qname"
  cupsaccept "$qname"
done

lpstat -p
