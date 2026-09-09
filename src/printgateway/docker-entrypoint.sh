#!/bin/sh
# Entrypoint for the printgateway image (see Dockerfile). POSIX sh, not bash:
# the runtime image is debian-slim, which does not install bash by default,
# and there is nothing here that needs it.
set -eu

# This container is CUPS-client-only — it never runs its own cupsd (see
# Dockerfile: only cups-client is installed, no cupsd binary even exists
# here). It always talks to a CUPS server elsewhere, which is normally the
# Linux host it runs on, reached via `docker run --network host`. CUPS_HOST
# defaults to "localhost" for exactly that reason: with host networking,
# "localhost" from inside the container IS the host, so this needs zero
# configuration to pick up every printer queue already set up there — no
# env var, no baked PPD, no hardcoded printer name. Override CUPS_HOST only
# when CUPS lives on a different box (e.g. reached over a bridge network).
#
# Why a client.conf file rather than the CUPS_SERVER environment variable
# (the more obvious Docker-native choice): internal/cups/lp.go deliberately
# builds the `lp` subprocess's environment from scratch —
#   cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
# — specifically so a secret this process holds (VAULT_TOKEN,
# SECRET_STORE_PASSWORD, S3 credentials) can never leak to `lp` or any CUPS
# filter it spawns via the process environment. CUPS_SERVER would be silently
# dropped by that same line, and "silently ignored" is a worse failure mode
# than "not supported" for something this security-sensitive — so this
# writes the remote server into libcups' own config file instead, which `lp`
# reads regardless of environment, with zero changes to that Go code.
CUPS_HOST="${CUPS_HOST:-localhost}"
CUPS_PORT="${CUPS_PORT:-631}"
echo "ServerName ${CUPS_HOST}:${CUPS_PORT}" > /etc/cups/client.conf
echo "docker-entrypoint: /etc/cups/client.conf -> ServerName ${CUPS_HOST}:${CUPS_PORT}"

# exec, not a plain call: this replaces the shell (PID 1 in the container)
# with the Go binary itself, so SIGTERM from `docker stop`/`docker compose
# stop` reaches the process directly. Without exec, PID 1 stays this shell,
# which does not forward signals to a child by default — `docker stop` would
# then hang for the full stop-timeout and kill -9 the binary, skipping the
# graceful-shutdown path (run()'s in-flight-request drain) entirely.
exec /usr/local/bin/printgateway "$@"
