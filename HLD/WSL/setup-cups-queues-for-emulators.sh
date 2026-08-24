#!/bin/bash
set -e
lpadmin -x vp 2>/dev/null || true
for n in 1 2 3; do
  port=$((9000 + n))
  lpadmin -x vp${n} 2>/dev/null || true
  lpadmin -p vp${n} -E -v ipp://127.0.0.1:${port}/ipp/print -m everywhere
  cupsenable vp${n}
  cupsaccept vp${n}
done
lpstat -p
