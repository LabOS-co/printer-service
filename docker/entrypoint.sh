#!/bin/bash
set -e

: "${PRINTER_HOST:?PRINTER_HOST env var is required (printer IP)}"
: "${PRINTER_QUEUE:=poc}"

# Allow the IPP interface to be reached from outside the container (default
# Debian config only listens on localhost), and open up access so the Go
# client on the Windows host can submit jobs without CUPS authentication.
sed -i 's/^Listen localhost:631/Listen *:631/' /etc/cups/cupsd.conf
sed -i '0,/<Location \/>/{s//<Location \/>\n  Allow all/}' /etc/cups/cupsd.conf
sed -i 's/^DefaultAuthType .*/DefaultAuthType None/' /etc/cups/cupsd.conf
# Docker's NAT means requests arrive from the bridge gateway rather than
# 127.0.0.1 even though the client used "localhost:631" as Host: header.
# CUPS rejects that Host mismatch by default (anti DNS-rebinding check).
echo "ServerAlias *" >> /etc/cups/cupsd.conf

mkdir -p /run/cups
/usr/sbin/cupsd -f &
CUPSD_PID=$!

until [ -S /run/cups/cups.sock ]; do
  sleep 0.5
done
sleep 1

if ! lpstat -p "$PRINTER_QUEUE" >/dev/null 2>&1; then
  echo "Setting up driverless (IPP Everywhere) queue '$PRINTER_QUEUE' -> ipp://$PRINTER_HOST/ipp/print"
  lpadmin -p "$PRINTER_QUEUE" -E -v "ipp://$PRINTER_HOST/ipp/print" -m everywhere
  cupsaccept "$PRINTER_QUEUE" 2>/dev/null || true
  cupsenable "$PRINTER_QUEUE"
fi

lpstat -p "$PRINTER_QUEUE" -l || true

wait "$CUPSD_PID"
