# Build context is src/printgateway, not the repo root:
#
#   cd src/printgateway && go mod vendor && docker build -t printgateway:latest .
#
# go.mod replace directives point outside this repo, so the image builds with
# -mod=vendor. Drop that flag once those branches are tagged and vendor/ is gone.
#
# Do not set GOOS/GOARCH and do not pin this stage with --platform=$BUILDPLATFORM.
# The stage follows --platform, and go build emits a binary for that architecture.
# Forcing GOARCH (or pinning the stage to the builder) puts an amd64 binary in
# the arm64 image, which fails on ARM with "exec format error".
# golang:1.25 tracks the newest patch in that line until the next pull.
FROM golang:1.25-trixie AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# VERSION/BUILD_TIME/COMMIT populate /status. Defaults match ldflags' zero value.
ARG VERSION=unknown
ARG BUILD_TIME=unknown
ARG COMMIT=unknown
# CGO_ENABLED=0: dependencies are pure Go, so the runtime stage does not need
# a matching glibc. The go-build cache is BuildKit-only and is not in the image.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 \
    go build -mod=vendor -trimpath -ldflags="-s -w \
      -X github.com/version-go/ldflags.buildVersion=${VERSION} \
      -X github.com/version-go/ldflags.buildTime=${BUILD_TIME} \
      -X github.com/version-go/ldflags.buildHash=${COMMIT}" \
    -o /out/printgateway ./cmd/printgateway

# cups-client only: this container runs lp against a CUPS server elsewhere.
# ca-certificates: outbound HTTPS (Vault, S3/MinIO, file_url) fails closed without them.
FROM debian:trixie-slim

LABEL org.opencontainers.image.title="LAB-16894 Print Gateway" \
      org.opencontainers.image.description="HTTP front end that submits print jobs to a CUPS queue via lp" \
      org.opencontainers.image.source="https://github.com/LabOS-co/printer-server" \
      org.opencontainers.image.licenses="UNLICENSED"

RUN apt-get update \
    && apt-get install -y --no-install-recommends cups-client ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -r -M -s /usr/sbin/nologin printgateway

COPY --from=build /out/printgateway /usr/local/bin/printgateway
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 755 /usr/local/bin/printgateway /usr/local/bin/docker-entrypoint.sh \
    && mkdir -p /etc/cups && chown printgateway:printgateway /etc/cups

USER printgateway

EXPOSE 8090/tcp

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
# Clears debian's default CMD ["bash"], which would otherwise be passed to printgateway.
CMD []
