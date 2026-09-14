# LAB-16894 Print Gateway — production image.
#
# Build context is THIS directory (src/printgateway), not the repo root:
#
#   docker build -t printgateway:latest src/printgateway
#
# ---------------------------------------------------------------------------
# PREREQUISITE, READ BEFORE BUILDING: two dependencies are not on a tagged
# release yet (see README.md's "labOS shared library" section) —
#
#   github.com/LabOS-co/go-packages/cloud_storage  (feature/cloud_storage/...)
#   github.com/LabOS-co/go-packages/secret_store   (feature/secret_store/...)
#
# go.mod resolves both via a local `replace` directive pointing OUTSIDE this
# repo (../../../go-packages/..., ../../../go-packages-secret_store-wt/...).
# Those paths do not exist inside a Docker build context, a fresh CI checkout
# of just this repo, or anyone else's machine — `go build`/`go mod download`
# fail hard the moment they're needed. This is not a hypothetical: it was
# verified by attempting exactly that build.
#
# Until both branches are merged and tagged (at which point the `replace`
# lines in go.mod go away entirely and this whole prerequisite disappears),
# this image is built from a VENDORED, self-contained snapshot instead:
#
#   cd src/printgateway
#   go mod vendor          # run on a machine that HAS both sibling checkouts
#   docker build -t printgateway:latest .
#
# `go mod vendor` resolves the replace directives once, copying their actual
# source into vendor/, and this Dockerfile builds with `-mod=vendor` — so the
# resulting image is reproducible without needing those sibling repos ever
# again. Verified: a vendored build compiles clean for linux/amd64.
#
# vendor/ is not committed by default (see .gitignore) since it embeds source
# from two unpublished branches; regenerate it per release, or commit it
# deliberately if you want a build that needs no local sibling checkouts at
# all — that decision belongs to whoever owns the release process, not this
# Dockerfile. Once both branches are tagged, drop `-mod=vendor` below, delete
# vendor/, and a plain `go mod download` takes over.
# ---------------------------------------------------------------------------

# --- build stage -------------------------------------------------------
# Deliberately NOT `FROM --platform=$BUILDPLATFORM ...`: that buildx-only
# directive lets this stage cross-compile natively instead of under qemu
# emulation, which is worth having when buildx is available, but it makes the
# Dockerfile unbuildable with the plain classic `docker build` (verified
# live: "invalid OS component" — BUILDPLATFORM resolves to empty without
# BuildKit/buildx). ARG TARGETOS/TARGETARCH below carry explicit defaults for
# exactly that reason: buildx still populates and can override them per
# target platform; the classic builder gets a working single-arch build
# instead of an empty, broken one.
FROM golang:1.25-bookworm AS build

WORKDIR /src

# go.mod/go.sum (and vendor/, if present) copied before the rest of the
# source: this layer is only invalidated when dependencies change, not on
# every source edit, which is most of the point of a multi-stage Dockerfile.
COPY go.mod go.sum ./
COPY vendor/ ./vendor/

COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG TARGETOS=linux
ARG TARGETARCH=amd64
# VERSION/BUILD_TIME/COMMIT: populate /status's version/build/label fields
# (see README's "Health check") via github.com/version-go/ldflags's package
# vars. Each defaults to "unknown" — exactly ldflags' own zero-value — so an
# `docker build` that doesn't pass them behaves identically to before this
# existed, just with an uninformative /status.
ARG VERSION=unknown
ARG BUILD_TIME=unknown
ARG COMMIT=unknown
# CGO_ENABLED=0: every current dependency (the Vault client, the MinIO client,
# error_handler/logs) is pure Go, so a static binary is possible and is what
# lets the runtime stage below be debian-slim rather than needing glibc's full
# dynamic-linking machinery matched exactly between stages.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -mod=vendor -trimpath -ldflags="-s -w \
      -X github.com/version-go/ldflags.buildVersion=${VERSION} \
      -X github.com/version-go/ldflags.buildTime=${BUILD_TIME} \
      -X github.com/version-go/ldflags.buildHash=${COMMIT}" \
    -o /out/printgateway ./cmd/printgateway

# --- runtime stage -------------------------------------------------------
FROM debian:bookworm-slim AS runtime

LABEL org.opencontainers.image.title="LAB-16894 Print Gateway" \
      org.opencontainers.image.description="HTTP front end that submits print jobs to a CUPS queue via lp" \
      org.opencontainers.image.source="https://github.com/LabOS-co/printer-server" \
      org.opencontainers.image.licenses="UNLICENSED"

# cups-client (not the full `cups` package): this container never runs its
# own cupsd. It is a print CLIENT only — it always talks to a CUPS server
# elsewhere (normally the Linux host it runs on, over --network host — see
# docker-entrypoint.sh) and inherits whatever printer queues are already
# configured there. Nothing printer-specific is baked into this image: no
# PPD, no `lpadmin`-created queue, no hardcoded printer name. Add a printer
# on the host's CUPS and it's immediately usable here, with no image rebuild.
# ca-certificates: this process makes outbound HTTPS calls of its own (Vault,
# S3/MinIO, an https:// file_url) — without a CA bundle those all fail closed
# with a certificate-verification error, which is correct behaviour but easy
# to mistake for a networking problem if this is forgotten.
# curl: used only by HEALTHCHECK below (see its comment for why a bare TCP
# probe is not enough); also handy for `docker exec` debugging.
RUN apt-get update \
    && apt-get install -y --no-install-recommends cups-client ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

# Client-only means no cupsd to bind privileged port 631 and no
# root-owned /var/spool/cups to manage, so this can run as a plain
# unprivileged user.
RUN useradd -r -M -s /usr/sbin/nologin printgateway

COPY --from=build /out/printgateway /usr/local/bin/printgateway
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 755 /usr/local/bin/printgateway /usr/local/bin/docker-entrypoint.sh \
    && mkdir -p /etc/cups && chown printgateway:printgateway /etc/cups

USER printgateway

# The repo-root printservice.config.json is NOT copied into this image — the
# build context is this directory (src/printgateway), not the repo root, so
# it is out of reach at build time by design (see this Dockerfile's header
# comment on build context). To use it: bind-mount the file into the
# container and set PRINT_GATEWAY_CONFIG to that in-container path, e.g.
#   docker run -v /host/path/printservice.config.json:/etc/printgateway/printservice.config.json:ro \
#     -e PRINT_GATEWAY_CONFIG=/etc/printgateway/printservice.config.json ...
# config.Load no longer takes a CMD/argv address override — address comes
# only from the PORT and PRINT_GATEWAY_BIND_HOST env vars (see README's
# "Health check"/"Access control" sections), and resource/printgateway.addr
# in a config file is a hard startup error rather than a fallback. A wrapper
# or compose file must set those env vars, not pass a bind address as CMD.

# Documentation only — does not itself publish the port. The default listen
# address is 0.0.0.0:8090 (PRINT_GATEWAY_BIND_HOST's default), which is what
# you want inside a container: Nomad/Consul/Traefik (or another container on
# the same compose network) must be able to reach it from outside this
# container's own network namespace. Set PRINT_GATEWAY_BIND_HOST=127.0.0.1 to
# restrict it to this container only — the container boundary itself (no
# published port, or a port published only to 127.0.0.1 on the host) is what
# should enforce "closed to everything but the calling system" in this
# deployment model, not the app's own bind address. See docker-entrypoint.sh
# and the compose file for how CUPS_HOST/the listen address are actually
# wired up.
EXPOSE 8090/tcp

# No Docker-level HEALTHCHECK here: the network-proxy in front of this
# service already polls the unauthenticated GET /status endpoint (see
# README's "Health check" section) every 30s as its own liveness check. A
# second, redundant HEALTHCHECK on the same interval inside the image would
# just duplicate that probe against the same process.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD []
