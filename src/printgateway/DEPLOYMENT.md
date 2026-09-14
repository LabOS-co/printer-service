# Print Gateway — Deployment Guide

This is the step-by-step runbook for getting `printgateway` up, running, and
able to receive `/print` requests — as a one-off local container, as a
manually-run test/staging instance, or as a Nomad job registered in Consul
and routed to by Traefik. It assumes zero prior context beyond having this
repo checked out.

For *what every setting does and why* (env vars, config file, Vault, S3,
auth, timeouts), see `README.md` — start with its **"Every setting, at a
glance"** table (right under its own top), which lists literally every env
var, CLI flag, and config-file key this service reads, each linked to the
section with the full explanation. This document only tells you *which
buttons to press, in which order*, for each target environment. Where a step
needs more depth than belongs in a runbook, it links to the matching
`README.md` section instead of repeating it.

**Scope reminder:** this is the hardened prototype described in
`README.md`'s "What's deliberately not here yet" section — a single-node
process, no queue/DLQ/audit trail. Deploying it (anywhere, including
production Nomad) does not change that; it just makes this prototype
reachable the way the target infrastructure expects.

---

## 0. Prerequisites — read this before any environment below

1. **A reachable CUPS server with the printer queue(s) already configured.**
   `printgateway` is a CUPS *client* only — it never runs its own `cupsd` and
   has no printer-onboarding logic. Before deploying this service at all,
   confirm the target CUPS instance already has the queue you intend to
   print to (`lpstat -p` on that host). See root `CLAUDE.md`'s "Build and
   run" and `docs/STATUS.md` for how the WSL/Ubuntu CUPS environment used in
   development was built; a test/prod deployment needs the equivalent
   already done on whatever host actually runs `cupsd` for that environment.
2. **Docker 24+** (or a Docker-API-compatible engine) on the machine that
   builds the image, and on every machine that runs it — including every
   Nomad client in the production case, since the job spec below uses
   Nomad's `docker` task driver.
3. **A Go 1.25 toolchain**, only on the machine that builds the image (not on
   any machine that just runs the resulting container) — needed for the
   `go mod vendor` step in §1 below.
4. **Decide your auth posture up front**: will this deployment set
   `PRINT_GATEWAY_REQUIRE_AUTH=true` and issue a real `PRINT_GATEWAY_TOKEN`
   (or wire Vault)? Production should. See `README.md`'s "Access control"
   and "Secrets (Vault)" sections. Have the token (or Vault address/creds)
   ready before §4 (production) below — the process refuses to start with
   auth required and no token resolvable from anywhere.
5. **Two Go dependencies are on unpublished branches**, not a tagged
   release, as of this writing (`README.md`'s "labOS shared library"
   section has the full detail): `go-packages/cloud_storage` and
   `go-packages/secret_store`. This is why the build in §1 vendors them —
   until both are merged and tagged, building this image requires having
   both sibling branches checked out next to this repo (see §1). If they
   have since been merged and tagged, `go.mod`'s `replace` lines will be
   gone and `go mod vendor` becomes unnecessary — check `go.mod` for a
   `replace github.com/LabOS-co/go-packages/...` line before assuming this
   step is still needed.

---

## 1. Build the image (every environment starts here)

Run this on a machine that has this repo **and**, until the two branches in
prerequisite 5 above are tagged, both sibling checkouts `go.mod` currently
points its `replace` lines at (`../../../go-packages/cloud_storage`,
`../../../go-packages-secret_store-wt/secret_store`).

```bash
cd src/printgateway

# Vendors the replace-directive source so the Docker build context is
# self-contained (Dockerfile builds with -mod=vendor). Skip this step only
# once go.mod no longer has any `replace github.com/LabOS-co/go-packages/...`
# line — at that point `go mod download` inside the Dockerfile is enough.
go mod vendor

VERSION=$(git describe --tags --always)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
COMMIT=$(git rev-parse --short HEAD)

docker build \
  --build-arg VERSION="$VERSION" \
  --build-arg BUILD_TIME="$BUILD_TIME" \
  --build-arg COMMIT="$COMMIT" \
  -t printgateway:"$VERSION" \
  -t printgateway:latest \
  .
```

Confirm it built and reports a real version (not required, but catches a
bad build immediately rather than at deploy time):

```bash
docker run --rm printgateway:latest /usr/local/bin/printgateway -h 2>/dev/null; echo "image OK: $VERSION"
```

**For production**, push the tagged image to whatever registry your Nomad
clients can pull from:

```bash
docker tag printgateway:"$VERSION" your-registry.example.com/printgateway:"$VERSION"
docker push your-registry.example.com/printgateway:"$VERSION"
```

Everything below assumes you have an image reference — either the local
`printgateway:latest` (Paths A/B) or a pushed `your-registry.../printgateway:$VERSION`
(Path C) — ready to run.

---

## Path A — Local / developer machine

Use this to run printgateway against your own dev CUPS instance, e.g. inside
the same WSL2 distro CUPS runs in.

```bash
cd src/printgateway
cp .env.example .env          # then edit .env: set PRINT_GATEWAY_TOKEN
```

Point the compose file at a real per-deployment config, or skip the config
file entirely (see README's "Configuration file" section — it's optional):

```bash
# repo root, git-ignored, only needed if you want the JSON config layer active
cp ../../printservice.config.json ../../printservice.config.local.json
```

```bash
docker compose up --build
```

This builds the image, bind-mounts `printservice.config.local.json`, and
runs with `network_mode: host` — correct **only** when Docker Engine itself
runs inside the same Linux/WSL2 network namespace as `cupsd` (see the
comments at the top of `docker-compose.yml` for how to tell, and the
Docker-Desktop bridge-network variant if it doesn't apply to your machine).

Verify:

```bash
curl -s http://localhost:8090/status
# {"status":"Up and running :-)","version":"...","build":"...","label":"..."}

curl -X POST http://localhost:8090/print \
  -F "printer=<a queue name from 'lpstat -p'>" \
  -F "file=@/path/to/some.pdf"
```

Stop with `docker compose down`.

---

## Path B — Test / staging environment

Two supported shapes for a test box: a plain `docker run` (fastest to stand
up, closest to what Nomad will ultimately run) or the systemd unit (closest
to "install it like a real service without container orchestration"). Pick
one — don't run both against the same CUPS instance and port at once.

### B1 — Plain Docker

On the test host (must have `cupsd` running, or reachable — see step 2):

1. **Get the image there.** Either build it on that host directly (§1), or
   `docker save`/`docker load` a tarball, or `docker pull` from a registry
   if you pushed one.
2. **Decide how the container reaches CUPS.** If Docker Engine on this host
   shares CUPS's own network namespace (native Docker on the same Linux/WSL2
   box as `cupsd`), use `--network host` and leave `CUPS_HOST` at its
   `localhost` default. Otherwise, set `CUPS_HOST=<the real CUPS host>` and
   publish the port explicitly instead of using host networking.
3. **Provision a token.** Generate a real secret for `PRINT_GATEWAY_TOKEN` —
   this is a test environment, not local dev, so don't reuse a token that
   might leak into a shared local `.env`.

```bash
docker run -d --name printgateway \
  --network host \
  --restart unless-stopped \
  -e PRINT_GATEWAY_REQUIRE_AUTH=true \
  -e PRINT_GATEWAY_TOKEN='<generated secret>' \
  -e PRINT_GATEWAY_BIND_HOST=0.0.0.0 \
  -e LOG_SERVER=<logstash-host>:514 \
  printgateway:latest
```

(Omit `--network host` and add `-p 8090:8090 -e CUPS_HOST=<cups-host>` if
CUPS is not on the same network namespace as this Docker Engine.)

**Optional: the JSON config file.** Everything above uses plain env vars,
which is enough for most deployments — skip this unless you specifically
need it (see the "Do you need `printservice.config.json`?" box under Path C
below; the same reasoning applies here). If you do want it, bind-mount the
file and point `PRINT_GATEWAY_CONFIG` at the in-container path:

```bash
docker run -d --name printgateway \
  --network host --restart unless-stopped \
  -e PRINT_GATEWAY_REQUIRE_AUTH=true \
  -e PRINT_GATEWAY_TOKEN='<generated secret>' \
  -e PRINT_GATEWAY_BIND_HOST=0.0.0.0 \
  -e PRINT_GATEWAY_CONFIG=/etc/printgateway/printservice.config.json \
  -v /host/path/printservice.config.local.json:/etc/printgateway/printservice.config.json:ro \
  printgateway:latest
```

Use the repo's git-ignored `printservice.config.local.json` (not the tracked
`printservice.config.json`) if it will carry a real S3 credential — see
`README.md`'s "Two files, two purposes". The tracked file is a secret-free
example; bind-mounting it changes nothing about your deployment's settings
(every value in it matches the compiled defaults).

Verify:

```bash
curl -s http://<test-host>:8090/status
curl -X POST http://<test-host>:8090/print \
  -H "X-Labos-Print-Token: <generated secret>" \
  -F "printer=<queue>" -F "file=@/path/to/some.pdf"
```

### B2 — systemd (no container)

Use this if the test environment mirrors the WSL/native-binary model instead
of Docker. From a machine with the Linux binary built (`GOOS=linux
GOARCH=amd64 go build ...` per `CLAUDE.md`/`README.md`'s "Building and
running"), copy `printgateway-linux-amd64` next to `printgateway.service` and
`printgateway.env.example` under `src/printgateway/` on the target host, then
as root:

```bash
cd src/ops
./install-services.sh
```

This creates the `printgateway` system user, installs the binary to
`/opt/printgateway/`, writes a blank `/etc/printgateway/printgateway.env`
(mode 600) if none exists yet, and installs the systemd unit — but does
**not** start it (per-deployment config has to be filled in first). It also
**installs the JSON config file automatically, if one exists** at the repo
root (`printservice.config.local.json`, preferred, else the tracked
`printservice.config.json`) — but does **not** turn it on:
`PRINT_GATEWAY_CONFIG` is left commented out in `printgateway.env` either
way, so having the file present on disk never silently changes behavior.
Uncomment it in `printgateway.env` only if you actually want the config-file
layer active (see the "Do you need `printservice.config.json`?" box under
Path C below). Then:

```bash
vi /etc/printgateway/printgateway.env      # set PRINT_GATEWAY_TOKEN, etc.
systemctl daemon-reload
systemctl enable --now printgateway
systemctl status printgateway
curl -s http://localhost:8090/status
```

---

## Path C — Production: Nomad + Consul + Traefik

The code is already wired for this (see `docs/STATUS.md`'s "Tenth phase" for
what changed and why): `PORT`/`PRINT_GATEWAY_BIND_HOST` replace any
positional CLI arg, `GET /status` answers `system_api`'s standard JSON
health contract at whatever port Nomad allocates, and the process binds
`0.0.0.0` by default so Consul/Traefik can reach it from outside the
allocating host's network namespace. What was missing until now was the
actual job spec — it lives at `src/printgateway/deploy/printgateway.nomad`.

### C1 — One-time per-cluster setup

- **Registry access.** Every Nomad client that can be scheduled for this job
  must be able to pull the pushed image (§1) — set up registry auth on the
  clients if the registry isn't public/already trusted.
- **Vault integration**, if this cluster uses Vault-backed secrets (see
  `README.md`'s "Secrets (Vault)" section) — the job spec's `template` stanza
  below assumes the Nomad cluster already has a Vault policy that can read
  `<LABOS_ENV>/config/print_gateway`. If this cluster instead uses a plain
  env-var secret, skip the `template`/`vault` stanzas and set
  `PRINT_GATEWAY_TOKEN` directly (e.g. via Nomad Variables — see the job
  spec's comments for both options side by side).
- **CUPS reachability from the Nomad client.** This is the one piece that's
  genuinely infrastructure-specific and not something this repo can decide
  for you: `printgateway` must be able to reach a real `cupsd` over the
  network from wherever Nomad schedules it. The job spec below defaults to
  `network_mode = "host"` so `CUPS_HOST=localhost` reaches a `cupsd` running
  directly on the same Nomad client — appropriate if every Nomad client that
  can run this job also runs (or is) the print server. If instead CUPS lives
  on a separate, fixed host reachable over the network, switch the job's
  network mode to `bridge` and set `CUPS_HOST`/`CUPS_PORT` to that host —
  see the job spec's comments at the `network` stanza for exactly what to
  change.

### C1.5 — Do you need `printservice.config.json`?

The job spec as written **does not use the JSON config file at all** —
every setting comes from the `env{}`/`vault{}`/`template{}` stanzas already
in it, which is the simplest correct production setup and the one you
should default to. Only add the config file on top of that if one of these
applies:

- **You want the S3 access/secret key pair rendered from Vault as a file**
  instead of as plain env vars. This is the one deployment model
  `README.md`'s "Configuration file" section calls out by name (a file
  "rendered from Vault at process start, e.g. a Vault Agent template") — and
  it maps directly onto Nomad's own `template{}` stanza with a `vault.read`
  or `vault.write` source, no separate Vault Agent needed. See the commented
  `template` block in `printgateway.nomad` (`# --- Optional: JSON config
  file ---`) for a working example: render `printservice.config.json` from
  a Vault KV path into the allocation's task directory, then set
  `PRINT_GATEWAY_CONFIG` to that rendered path.
- **You want to override a timeout/limit/S3 setting per-cluster without
  redefining a env var for it** (e.g. a shared `resource/printgateway`
  block checked into a per-environment overlay repo, outside this one).

If neither applies — the common case — leave the config file out entirely.
Adding it later is a config-only change (no code, no image rebuild): render
the file, set `PRINT_GATEWAY_CONFIG`, redeploy the job. Removing it later
needs the same "roll file and binary together" care described in
`README.md`'s "Configuration file" → "Rollback" note — an older binary
refuses to start against a file naming a key it doesn't recognize.

### C2 — Deploy

```bash
cd src/printgateway/deploy
# edit printgateway.nomad: image reference/tag, datacenter/namespace names,
# CUPS_HOST if not using host networking, Traefik hostname rule — every spot
# needing a per-cluster value is marked with a "CHANGE ME" comment.

nomad job plan printgateway.nomad     # review the diff before applying
nomad job run printgateway.nomad
```

### C3 — Verify it's actually serving

```bash
nomad job status printgateway
nomad alloc status <alloc-id>                 # confirm the task is "running" and healthy

consul catalog services                        # "printgateway" should be listed
consul health service printgateway            # should show passing, not critical

# Through Traefik, from wherever the router is reachable:
curl -s https://<traefik-hostname-from-the-job-spec>/status
curl -X POST https://<traefik-hostname>/print \
  -H "X-Labos-Print-Token: <the real token>" \
  -F "printer=<queue>" -F "file=@/path/to/some.pdf"
```

If Consul shows the service **critical** rather than passing, re-check the
network-mode/CUPS-reachability decision in C1 first — a common cause is the
health check reaching the container fine while the *advertised* address is
wrong (see `README.md`'s explicit warning: never combine `-consul-register`
with a loopback bind host — this job spec does not pass `-consul-register`
at all, since Nomad's own `service` stanza does the Consul registration;
that flag is a local-dev-only convenience, not something production uses).

### C4 — Rolling out a new version

Standard Nomad update: push the new image tag (§1), bump the `image` line in
`printgateway.nomad`, then `nomad job run` again — the job's `update` stanza
(canary + health check) handles the rollout. If the new version adds a
**config-file key** (see `README.md`'s "Configuration file" → "Rollback"
note), roll the config file forward first, deploy the binary second; roll
back in the opposite order — an older binary refuses to start against a
newer file it doesn't recognize a key from.

---

## 2. Post-deploy smoke test (run this in every environment)

```bash
curl -s http://<host>:<port>/status
```

Expect `200` with `"status":"Up and running :-)"`. Then, with a real queue
name from `lpstat -p` on the CUPS host this deployment points at:

```bash
curl -X POST http://<host>:<port>/print \
  -H "X-Labos-Print-Token: <token, if PRINT_GATEWAY_REQUIRE_AUTH=true>" \
  -F "printer=<queue>" \
  -F "file=@/path/to/some.pdf"
# {"status":"submitted","output":"request id is <queue>-<n> (0 file(s))\n"}
```

A `401` here with auth enabled means the token/Vault path isn't resolving —
check the startup log line naming which source won (`README.md`'s "Secrets
(Vault)" section). A `503` on the S3 endpoints (`s3_key`, `/files/presign`)
without a `file`/`file_url` request is expected if S3 isn't configured — that
is additive and off by default, not a deployment error.

---

## 3. Troubleshooting

| Symptom | Likely cause |
| :--- | :--- |
| Container starts, `/status` unreachable | Check the network-mode/port decision (Path A/B/C) — a bridge network without a published/mapped port is the most common cause. |
| `/print` returns 500, "No file in print request" or a `lp` error | `printer` doesn't name a real CUPS queue reachable from where this container's `CUPS_HOST` points — run `lpstat -p` against that CUPS host directly to confirm the queue exists. |
| Consul shows the service `critical` | See C3 above — usually a mismatch between the advertised address and where the health check is actually reachable from, or `-consul-register` combined with a loopback bind host. |
| Process refuses to start, "no token" error | `PRINT_GATEWAY_REQUIRE_AUTH=true` with no `PRINT_GATEWAY_TOKEN` and no working Vault path — see `README.md`'s "Fallback policy". |
| `docker build` fails resolving `go-packages/cloud_storage` or `secret_store` | Prerequisite 5 / §1 — those two packages aren't tagged yet; `go mod vendor` must run on a machine with both sibling branches checked out first. |
| A previously-working config file now fails startup with "unknown field" | See "Rollback" in `README.md`'s "Configuration file" section — roll the file and binary forward/back together, never independently. |
