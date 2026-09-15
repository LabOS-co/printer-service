# Print Gateway — initial prototype

## What this is

A minimal HTTP server that accepts a print request — a printer name and a
file path — and prints it. It's a prototype meant to prove the basic
request-in / paper-out path end to end, not the production Gateway
described in `print-gateway-hld-phase1.docx`. It has none of that
document's HA, queue, Audit, retry, or security layers yet.

**Deploying this?** See `DEPLOYMENT.md` for the step-by-step runbook —
local, test/staging, and production (Nomad + Consul + Traefik, including a
ready-to-run job spec at `deploy/printgateway.nomad`). This README documents
*what every setting does*; `DEPLOYMENT.md` documents *which steps to run*.

## Every setting, at a glance

One row per env var/flag/config-file key this service reads, so nothing has
to be discovered by grepping the code. "Detail" links to the section with
the full explanation, default, and interactions — read this table to know
*what exists*, read the linked section before actually changing one.

| Setting | Kind | Default | Detail |
| :--- | :--- | :--- | :--- |
| `PORT` / `PRINT_GATEWAY_PORT` | env | `8090` | "Access control" |
| `PRINT_GATEWAY_BIND_HOST` | env | `0.0.0.0` | "Access control" |
| `PRINT_GATEWAY_REQUIRE_AUTH` | env | `false` | "Access control" |
| `PRINT_GATEWAY_TOKEN` | env / Vault / file | *(none)* | "Access control", "Secrets (Vault)" |
| `PRINT_GATEWAY_CONFIG` | env | *(unset = feature off)* | "Configuration file" |
| `PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS` | env-only (never file) | `false` | "SSRF defense (`file_url`)" |
| `PRINT_GATEWAY_FETCH_ALLOWED_HOSTS` | env / file | *(empty = no allowlist)* | "SSRF defense (`file_url`)" |
| `PRINT_GATEWAY_FETCH_TIMEOUT` | env / file | `60s` | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_FETCH_MAX_BYTES` | env / file | 64 MiB | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_READ_HEADER_TIMEOUT` | env / file | `10s` | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_READ_TIMEOUT` | env / file | `5m` | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_WRITE_TIMEOUT` | env / file | `8m` | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_IDLE_TIMEOUT` | env / file | `60s` | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_MAX_HEADER_BYTES` | env / file | 64 KiB | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_SHUTDOWN_GRACE` | env / file | `2m` | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_SUBMIT_TIMEOUT` | env / file | `30s` | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_MAX_UPLOAD_BYTES` | env / file | 64 MiB | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_MAX_JSON_BYTES` | env / file | 8 KiB | "Timeouts, limits, and shutdown" |
| `PRINT_GATEWAY_PRESIGN_TTL` | env / file | `15m` | "S3/MinIO object storage" |
| `PRINT_GATEWAY_LOG_LEVEL` | env / file | `info` | "Logging" |
| `LOG_SERVER` | env / Vault / file | *(unset = console-only)* | "Logging" |
| `HOST_NAME` | env | *(unset)* | "Logging" |
| `HOST_IP` | env | *(unset)* | "Logging" |
| `PRINT_GATEWAY_S3_ENDPOINT` | env / file | *(empty = disabled)* | "S3/MinIO object storage" |
| `PRINT_GATEWAY_S3_BUCKET` | env / file | *(empty = disabled)* | "S3/MinIO object storage" |
| `PRINT_GATEWAY_S3_REGION` | env / file | *(empty, not recommended)* | "S3/MinIO object storage" |
| `PRINT_GATEWAY_S3_INSECURE` | env / file | `false` | "S3/MinIO object storage" |
| `PRINT_GATEWAY_S3_ACCESS_KEY` / `_SECRET_KEY` | env / Vault / file | *(none)* | "S3/MinIO object storage" |
| `PRINT_GATEWAY_S3_TIMEOUT` | env / file | `60s` | "S3/MinIO object storage" |
| `PRINT_GATEWAY_S3_MAX_BYTES` | env / file | 64 MiB | "S3/MinIO object storage" |
| `VAULT_ADDR` / `SECRET_STORE_URL` | env | *(unset = Vault unused)* | "Secrets (Vault)" |
| `VAULT_TOKEN` | env | *(none)* | "Secrets (Vault)" |
| `SECRET_STORE_USERNAME` / `SECRET_STORE_PASSWORD` | env | *(none)* | "Secrets (Vault)" |
| `LABOS_ENV` | env | *(unset = no path prefix)* | "Secrets (Vault)" |
| `CUPS_HOST` / `CUPS_PORT` | **Docker entrypoint only**, not read by the Go binary | `localhost` / `631` | "Which CUPS server `lp` talks to" |
| `-consul-register` / `-consul-addr` | CLI flag (`system_args`) | off / `localhost:8500` | "Health check" |
| `-port`, `-p`, `-env`, `-gateway`, `-publishers`, `-services`, `-version`/`-v` | CLI flags (`system_args`), **live but inert** | n/a | "Health check" |

**Never a setting of this service, on purpose:** `CONSUL_REGISTER` (does not
exist — only the `-consul-register` flag works, despite the name suggesting
an env var); `allowPrivateTargets`/Consul/Redis keys in the config file (see
"Configuration file"'s "Never valid in the file, on purpose").

## How it prints

It does the simplest thing that's consistent with everything already
validated in this project: it shells out to CUPS's own `lp` command —

```
lp -d <printer-name> <path>
```

`<printer-name>` must already exist as a CUPS queue (`lpadmin`-configured,
with a static PPD, and behind `ippfix` if that specific printer needs it —
exactly the setup already built and tested in `src/ops/setup-15-queues.sh` /
`src/ops/setup-cups-queues-for-emulators.sh`). This server does not talk IPP
itself, does not know about PPDs/media/resolution, and does not decide
whether a printer needs `ippfix` — CUPS's own queue configuration handles
all of that already. If you want to print to a printer that doesn't have a
queue yet, set one up the same way the existing ones were set up, then use
that queue's name here.

### Which CUPS server `lp` talks to

`lp` (and every CUPS client tool) resolves its target server from
`/etc/cups/client.conf`'s `ServerName` line, or `localhost:631` if that file
doesn't set one — this service's own Go code never reads a "which CUPS host"
setting itself; it only ever runs `lp -d <printer> ...` and lets libcups
resolve the server the normal CUPS-client way.

- **Running the Docker image**: `docker-entrypoint.sh` writes
  `/etc/cups/client.conf` from two env vars on every container start —
  `CUPS_HOST` (default `localhost`) and `CUPS_PORT` (default `631`). With
  `--network host` (the default in `docker-compose.yml` and in
  `deploy/printgateway.nomad`), `localhost` from inside the container already
  *is* the host, so no override is needed when `cupsd` runs there too. Set
  `CUPS_HOST` (and `CUPS_PORT` if not `631`) only when CUPS lives on a
  different host, or when running with a bridge network (e.g.
  `CUPS_HOST=host.docker.internal` on Docker Desktop — see "Deployment
  notes" below). **These two are container-entrypoint variables, not
  variables this Go binary parses** — they have no effect on a native
  (non-Docker) run.
- **Running the native binary** (systemd or a bare `./printgateway-linux-amd64`):
  there is no `CUPS_HOST`/`CUPS_PORT` equivalent read by this service. CUPS
  is resolved the plain CUPS-client way — `localhost:631` unless the host's
  own `/etc/cups/client.conf` says otherwise. If `cupsd` isn't on the same
  host, set that file's `ServerName` yourself; this service has no setting
  for it.

## Request format — two options (per section 6 of the HLD doc)

`POST /print`, in one of two ways:

**Option 1 — attach the file directly** (`multipart/form-data`):

```bash
curl -X POST http://localhost:8090/print \
  -F "printer=q-hp-laserjet" \
  -F "file=@/home/youruser/invoice.pdf"
```

Fields: `printer` (text) and `file` (the file part). The server saves it to
a temp file, prints it, then deletes the temp file.

**Option 2 — send only a reference** (`application/json`):

```bash
curl -X POST http://localhost:8090/print \
  -H "Content-Type: application/json" \
  -d '{"printer":"q-hp-laserjet","file_url":"https://your-minio-or-s3/presigned-url..."}'
```

Fields: `printer` and `file_url`. The server downloads the file itself
before printing — built for a presigned S3/MinIO URL (the pattern the HLD
doc recommends), but it's a plain HTTP(S) GET under the hood, so any
directly-fetchable URL works. **No AWS SDK or credentials are involved on
this server** — the presigning/authorization has to already be baked into
the URL you pass in.

**Option 3 — reference a key in the configured object store** (`application/json`):

```bash
curl -X POST http://localhost:8090/print \
  -H "Content-Type: application/json" \
  -d '{"printer":"q-hp-laserjet","s3_key":"invoices/invoice-42.pdf"}'
```

Fields: `printer` and `s3_key`. The server downloads the object itself, from
the one bucket configured at startup (see "S3/MinIO object storage" below) —
**this is the recommended option for large files**, since option 1's upload
still has to cross this server's own connection first. Requires
`PRINT_GATEWAY_S3_ENDPOINT`/`PRINT_GATEWAY_S3_BUCKET` to be configured;
otherwise this answers `503`. `file_url` and `s3_key` are mutually
exclusive — sending both is a `400`.

`printer` (the CUPS queue name — run `lpstat -p` to see what's available) is
required in all three options.

`copies` (optional, all three options - a form field in option 1, a JSON
number in options 2/3) requests that many copies of the same document as a
single CUPS job (`lp -n`), not N separate submissions. Omitting the field
(or sending JSON `null`, treated the same as omitted), or sending `1`,
behaves exactly as before `copies` existed. A non-numeric value, an empty
value, or a value outside `1`-`100` is a `400`.

```bash
curl -X POST http://localhost:8090/print \
  -F "printer=q-hp-laserjet" \
  -F "file=@/home/youruser/invoice.pdf" \
  -F "copies=3"
```

The two JSON bodies (options 2/3, and `/files/presign` below) are decoded
strictly: a field name not listed above is a `400`, as is any content after
the one JSON value (a caller accidentally concatenating two bodies, for
example). Field-name matching stays case-insensitive and a duplicate key
still resolves last-value-wins, both standard `encoding/json` behavior this
server does nothing to change — only a genuinely unrecognized field name is
rejected.

Success response (either option):

```json
{"status":"submitted","output":"request id is q-hp-laserjet-42 (0 file(s))\n"}
```

Failure returns a non-2xx HTTP status with a labOS-standard error envelope
(same shape every other labOS Go service returns, via
`github.com/LabOS-co/go-packages/error_handler` — see below):

```json
{"errorCode":"21","errorDetails":{"details":"missing url parameter: printer"},"errorMessage":"Internal server error"}
```

The failure detail is in `errorDetails.details`; check the server's own log
output too — every request (success or failure) is logged there as well.

A request body over the configured size limit (see "Timeouts, limits, and
shutdown" below) gets `413`, naming the limit in `errorDetails.details`,
rather than the generic `400` every other malformed-body case gets — the two
are otherwise indistinguishable from the response alone.

## Health check

`GET /status`, unauthenticated (no `X-Labos-Print-Token` needed) — mounted by
`github.com/LabOS-co/go-packages/system_api.Register`, the same package every
other Nomad-orchestrated labOS Go service uses, so Nomad's own health check
and Consul/Traefik agree on one contract across services. Always `200 OK`
with a JSON body:

```json
{"status":"Up and running :-)","version":"1.2.3","build":"2026-09-09T12:00:00Z","label":"abc1234"}
```

`version`/`build`/`label` read `"unknown"` unless the binary was built with
`-ldflags` populating `github.com/version-go/ldflags`'s `buildVersion`/
`buildTime`/`buildHash` — see "Building and running" below.

There is currently no failure condition (no job-timeout or out-of-memory
check) that would turn this into a `5xx` — it only reports that the process
is up and serving.

Separately, `main()` always calls `system_args.ShouldRegisterToConsul()` (not
`system_api.Status`, which only reads `ldflags` package vars and never
touches `system_args`) and passes the result into `run()`. When it's true —
the **`-consul-register` command-line flag** is set — `run()` calls
`system_api.Register` (against a throwaway router; the real `GET /status`
route is already mounted above via `system_api.Status` directly) in its own
goroutine once the listener is already being served, purely for its other
effect: self-registering this service to a local Consul agent
(`localhost:8500`, or `CONSUL_ADDR`/`-consul-addr` if that fails) on startup.
**There is no `CONSUL_REGISTER` environment variable** — `system_args` never
reads one, despite the name suggesting otherwise; only the flag works. This
is a **local-dev convenience only** — under Nomad, `-consul-register` is
simply never passed, so this branch never fires; real production
registration is owned by the Nomad job spec, not this binary. The decision
is resolved in `main()`, not `run()`: `system_args.parseArgs()` runs the
global `flag.Parse()` against `os.Args` exactly once via `sync.Once`, and
`run()` is what `main_test.go`'s tests call directly with a test binary's own
`os.Args` — reaching `system_args` from there would parse `-test.*` flags it
never registered and `os.Exit(2)` the whole test binary. The registration
call itself is deliberately not in `httpapi.NewServer` either (every handler
test calls that): `NewServer` stays a pure builder with no network I/O, and
runs in its own goroutine off the startup path rather than blocking `run()`
because `system_api`'s HTTP client has no timeout — a stalled Consul agent
must not be able to delay serving or shutdown.

There is currently no deregistration on shutdown, so a stopped local-dev
instance stays registered and "critical" in Consul for the full 10-minute
`DeregisterCriticalServiceAfter` window — accepted for this local-dev-only
path rather than handled.

**Do not combine `-consul-register` with `PRINT_GATEWAY_BIND_HOST=127.0.0.1`**
(e.g. the `docker-compose.yml` local-dev setup): registration always
advertises the machine's own outbound-route LAN address
(`system_api`'s `getLocalIPAddress`), never `PRINT_GATEWAY_BIND_HOST`, so a
loopback-only process registers a health-check URL nothing is actually
listening on at that address — Consul then reports the service permanently
critical. `-consul-register` only makes sense together with the `0.0.0.0`
default bind host.

Calling `system_args.ShouldRegisterToConsul()` unconditionally from `main()`
also makes `system_args`' own flag surface (`-port`, `-p`, `-env`,
`-gateway`, `-consul-addr`, `-consul-register`, `-publishers`, `-services`,
`-version`/`-v`) live on the binary, and resolved early enough (before any
resource — Vault token, listener — is acquired) that an unrecognized flag's
`os.Exit(2)` fails startup cleanly instead of mid-`run()`. Accepted, not
worked around, but every one of those flags except `-consul-register`/
`-consul-addr` is otherwise **inert**: this service reads its own port from
`PORT`/`PRINT_GATEWAY_PORT` (see "Access control" below), never from
`-port`/`-p`, and nothing calls `system_args.DisplayVersion()`, so
`-version`/`-v` prints nothing.

## Access control

The server is closed to anything that is not the calling system, in two
independent layers — the same model `HtmlToPdf` relies on, plus a token:

1. **Listen address.** It binds `0.0.0.0:8090` by default — every interface —
   since under Nomad the port is dynamically allocated and Consul/Traefik
   must be able to reach it from outside the allocating host's own network
   namespace. Set `PORT` (or the higher-precedence `PRINT_GATEWAY_PORT`) to
   change the port, and `PRINT_GATEWAY_BIND_HOST` to restrict which interface
   it binds — set it to `127.0.0.1` for a manual local run that should stay
   closed to every other machine, matching this module's original loopback-only
   default. Neither is settable from the config file (see "Configuration
   file" below): like `PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS`, the listen
   address must come from the environment a process was actually launched
   with, not a file that's easier to leave stale.
2. **Shared token, opt-in.** `PRINT_GATEWAY_REQUIRE_AUTH` gates this whole
   mechanism and defaults to `false` — this deployment doesn't issue tokens to
   callers yet, so `/print` and `/files/presign` accept any request
   unauthenticated. Set it to `true` once a token is actually provisioned:
   every request must then carry the header `X-Labos-Print-Token`, matched in
   constant time against the resolved print token (`PRINT_GATEWAY_TOKEN`, or
   Vault — see "Secrets (Vault)" below). A missing or wrong token is `401`,
   and it is logged with the caller's address. With auth required and no
   token resolvable from any configured source, the process refuses to start
   rather than serving unauthenticated or answering `503` to everything
   forever — see "Fallback policy" below.

The token never appears in this repository or in the labOS source. On the
labOS side it comes from `gSecretManager` (`config/pdf_printer`, keys
`host` and `print-token` — `EnvironmentConfigurationOld::GetPdfPrinterParams`),
with a fallback to the `Autolims.cfg` keys `PDF_PRINTER_ADDRESS` /
`PDF_PRINTER_TOKEN` when that Vault path has no value for either key (or
Vault itself is unreachable). **This is a different Vault path from the one
this server reads its own token from** (`config/print_gateway`, key
`auth-token` — see "Secrets (Vault)" below) — the two sides do not share a
single KV entry; whoever provisions Vault must put the same token string
under both paths for the shared-secret check to actually match. Here it
comes from the environment, or from Vault when configured — see "Secrets
(Vault)" below.

### Configuration file

Every setting — including, as one deliberate exception explained below, the
S3 credential pair — can be set from a JSON config file, shaped as the
`resource/*` convention other labOS services already use for their own
config files (e.g. `OperationsService`'s), not a shape invented for this
service alone: cross-cutting infrastructure (the S3 connection, the
logstash destination) gets its own top-level `resource/*` block, shared in
spirit with whatever else on the same host reads it, while everything
specific to running *this* service lives under `resource/printgateway`.
`printservice.config.json` at the repo root is the **tracked, committed**
working example: it reproduces today's defaults exactly and must never
carry a real secret value (see "Two files, two purposes" below for the
git-ignored counterpart that does). Discovery is **explicit-only**: the
file is read if and only if `PRINT_GATEWAY_CONFIG` names its path — there
is no default path and no probing, so a deployment that never sets that
variable is completely unaffected by this feature, byte-for-byte identical
to before it existed.

**Precedence: config file → environment variable → compiled default.**
Every setting the file names wins over its env var unconditionally; every
setting the file omits (or the file itself is entirely absent) falls
through to the env var, then the compiled default, exactly as before. The
listen address (`PORT`/`PRINT_GATEWAY_PORT`/`PRINT_GATEWAY_BIND_HOST`) is
the one exception, in the other direction: it's env-only, with no config-file
key at all — a leftover `resource/printgateway.addr` key from before this
was env-only fails fast with a message naming what to set instead, rather
than a bare "unknown field" error.

```json
{
  "resource/log":         { "host": "logstash.internal", "port": "514" },
  "resource/file_storage": { "host": "s3.eu-west-1.amazonaws.com", "s3-user": "...", "s3-password": "..." },
  "resource/printgateway": {
    "logLevel": "info",
    "timeouts":    { "readHeader": "10s", "read": "5m", "write": "8m",
                     "idle": "60s", "shutdownGrace": "2m", "submit": "30s" },
    "limits":      { "maxHeaderBytes": 65536, "maxUploadBytes": 67108864, "maxJsonBytes": 8192 },
    "fetch":       { "timeout": "60s", "maxBytes": 67108864 },
    "objectStore": { "insecure": false, "timeout": "60s", "maxBytes": 67108864, "presignTtl": "15m" }
  }
}
```

Every top-level `resource/*` block is optional, and so is every key within
one — an absent key leaves its env var (or default) in effect. Durations
are `time.ParseDuration` strings (`"9m"`, `"90s"`); byte sizes are plain
JSON integers, not strings (`65536`, never `"64KiB"`), and a fractional
number (`65536.5`) is rejected the same as a non-numeric one. There is no
`version` key — this format has no future-compat version gate at all,
matching the `resource/*` convention it mirrors exactly rather than adding
a key that convention doesn't have.

**`""` is not the same as "key not present", for three keys only:**
`resource/file_storage.host`, `resource/printgateway.objectStore.bucket`,
and `resource/printgateway.objectStore.region` treat an explicit `""` as a
deliberate, meaningful value that **suppresses** the corresponding env var
(e.g. `"resource/file_storage": {"host": ""}` is how an operator disables
object storage from the file without touching the deployment's env vars) —
*omitting* the key entirely is what leaves the env var in effect.
`resource/printgateway.fetch.allowedHosts` follows the same rule at the
list level: an explicit `[]` means "no allowlist" (suppresses the env
var), while omitting the key leaves it in effect. `resource/log`'s `host`
and `port` follow it too, but only together: if BOTH resolve to empty
string the combined `LOG_SERVER` value is suppressed; naming only one side
is passed through for `LOG_SERVER`'s own parser to reject as malformed
(the same fail-fast every other bad value gets), not treated as a partial
suppression. **`resource/printgateway.logLevel` is the one exception to the
exception**: an explicit `""` there is a startup error, not a suppression,
since a blank log level is meaningless. Getting an equivalent version of
this wrong in an earlier draft of the shipped example was caught in review
before it shipped — `printservice.config.json` deliberately omits
`resource/log` and `resource/file_storage` entirely rather than setting
their fields to `""`, so that committing it and pointing
`PRINT_GATEWAY_CONFIG` at it changes nothing about S3/logstash
configuration.

**The one deliberate exception to "secrets never go in this file":**
`resource/file_storage.s3-user`/`s3-password` (env vars
`PRINT_GATEWAY_S3_ACCESS_KEY`/`PRINT_GATEWAY_S3_SECRET_KEY`) ARE valid file
keys, same `""`-suppresses-env rule as `resource/file_storage.host`. This
exists for a specific deployment model: the file itself rendered at
process start from Vault (e.g. a Vault Agent template writing the JSON
just before the service starts), not committed or hand-edited — the
opposite of every other setting's "safe to commit an example" property.
Precedence for these two is otherwise unchanged: `secrets.ResolveS3Credentials`
still tries Vault first and only falls back to whichever of file/env
supplied a value after that (the startup log names the winning source as
`vault`, `env`, `file`, or — if one credential came from the file and the
other from the env, a mixed state worth calling out on its own rather than
picking one — `file+env`). Blanking a credential in the file (`"s3-user":
""`) suppresses a stale env value the same way `resource/file_storage.host:
""` does, and does so **fail closed**: `ResolveS3Credentials` then returns
no usable credential at all (object storage disables itself, logging
`credentials-source` and naming whichever source actually supplied — or
blanked — each half), never a resurrected stale value.

**If you put a real credential here, treat this file exactly like
`printgateway.env`: mode `600`, never committed, generated per-deployment.**
`install-services.sh` installs it at mode `600` owned by the `printgateway`
service user specifically because of this — note that is a *different*
owner than `printgateway.env` (`root:root`), since this file is read
directly by the running Go process (`config.Load`'s `readFile`), not by
`systemd` before the process starts.

**Two files, two purposes — do not put a real credential in the tracked
one.** `printservice.config.json` (tracked, committed) must always stay the
secret-free working example above. A real deployment that puts an actual S3
credential in this file uses `printservice.config.local.json` instead — a
git-ignored name reserved for exactly this (see `.gitignore`) —
and `install-services.sh` prefers it automatically when present, falling
back to the tracked example only when it is absent. This is a mechanical
guard, not just a naming convention: a credential written under the
`.local.json` name cannot land in a commit by accident the way editing the
tracked file in place could.

**Never valid in the file, on purpose:** the print token, every Vault/
`secret_store` bootstrap variable (`VAULT_ADDR`, `SECRET_STORE_URL`,
`VAULT_TOKEN`, `SECRET_STORE_USERNAME`/`PASSWORD`, `LABOS_ENV`), and — the
one enforced at the type level, not just by convention —
`PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS`. There is no `allowPrivateTargets`
field anywhere in the Go type the file decodes into, so naming it in the
JSON (at the top level or nested under `resource/printgateway`) fails
exactly like any other typo: `json: unknown field "allowPrivateTargets"`.
This is deliberate, not an oversight — putting it in the file would make a
total SSRF bypass a config-file toggle instead of the environment-only,
loudly-logged knob it is today. The S3 access/secret key pair is the one
secret pulled out of this list — see above. Also absent, on purpose:
Consul (`resource/service_discovery`) and Redis (`resource/cache`), which
the same reference `resource/*` convention includes for services that use
them — printgateway uses neither, so there is no field for either anywhere
in this package's type tree, and naming them is an unknown-field error the
same way `allowPrivateTargets` is, not an inert placeholder.

A named-but-missing, named-but-unreadable, or malformed file — an unknown
field, a wrong JSON type, or a non-positive duration or byte size — is a
startup error naming both `PRINT_GATEWAY_CONFIG` and the path, the same
fail-fast policy every env override already follows; it is never silently
ignored, and it never silently falls back to `{}`. An empty file, or a
file containing a literal JSON `null`, is also rejected explicitly, rather
than reported as a bare decoder error or accepted as equivalent to `{}` —
both are realistic "a bind mount stopped being mounted" failure modes, not
hypothetical ones. Field-name matching stays case-insensitive under the
strict decoder (`"MaxUploadBytes"` is accepted the same as
`"maxUploadBytes"`), and a duplicate group key **merges** rather than
erroring or discarding either occurrence
(`{"resource/printgateway":{"timeouts":{"write":"9m"}},"resource/printgateway":{"timeouts":{"read":"4m"}}}`
sets both) — see "What's deliberately not here yet" above; both are
deferred to a future JSON Schema pass.

**One surprising interaction, worth repeating from the timeouts table
below:** the env var behind a setting is still validated even when the
config file supersedes it. A malformed `PRINT_GATEWAY_WRITE_TIMEOUT` still
fails startup even if the file's `resource/printgateway.timeouts.write` is
valid and would have won anyway — deliberate, so a stale or fat-fingered
env var left behind after migrating a setting into the file is caught
immediately rather than resurfacing silently the day the file is removed.

Every setting a file actually supplies is named in one startup log line
(`config file <path> supplied: <keys>`), logged loudly enough to survive a
warn/error `PRINT_GATEWAY_LOG_LEVEL` — the same treatment the
`PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS` warning gets, and for the same reason:
precedence here is inverted relative to 12-factor and every ops reflex, so
an operator debugging why an env var "has no effect" needs a one-line
answer, not an incident. Every other startup message that already names an
env var (a `validate` failure, the `AllowPrivateTargets` warning, an S3
misconfiguration) is relabeled to show the file's `<path>:<jsonPath>`
instead whenever the file is what actually supplied that value — except
`PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS`'s own line, which always shows the
bare env var, because no file label could ever legitimately apply to it.

**Known gap:** `ResolveLogServer`'s startup line still reports its source
as `env` even when the config file is what actually supplied
`resource/log`'s host/port — see "Logging" below. Fixing it is a
follow-up, not done in this change.

No `-config` command-line flag exists — `PRINT_GATEWAY_CONFIG` is the only
way to name the file. This was a deliberate scope decision for the first
version, not an oversight.

**Deployment notes:**

- **Docker:** the build context is `src/printgateway` (see `Dockerfile`), so
  neither `printservice.config.json` nor `printservice.config.local.json` is
  baked into the image. Bind-mount whichever one applies (the `.local.json`
  one if it carries a real S3 credential — see "Two files, two purposes"
  above) and set `PRINT_GATEWAY_CONFIG` to wherever it's mounted. The listen
  address is set via `PORT`/`PRINT_GATEWAY_BIND_HOST` env vars now, not a
  positional argument — `docker-entrypoint.sh`'s `exec … "$@"` passes through
  whatever args it's given, but printgateway itself no longer reads any.
  **`docker-compose.yml`** (same directory) wires this automatically for
  local/dev use: `docker compose up` builds the image, bind-mounts
  `printservice.config.local.json` to `/etc/printgateway/printservice.config.json`,
  sets `PRINT_GATEWAY_CONFIG` to that path, and reads `PRINT_GATEWAY_TOKEN`
  from a git-ignored `.env` file (copy `.env.example` and fill it in) —
  so the config file lands inside the container just by running it, no
  flags to remember. Runs on the default bridge network with
  `CUPS_HOST=host.docker.internal` (verified live), NOT `network_mode:
  host` — on Docker Desktop (Windows/Mac) the engine runs inside its own
  dedicated WSL2 distro, separate from whatever distro actually runs CUPS,
  so host networking would put the container in the *wrong* network
  namespace; `host.docker.internal` is the address Docker Desktop always
  routes to the real host regardless of which distro/VM anything runs in.
  Requires `go mod vendor` to have been run first (see the `Dockerfile`'s
  own header comment) and, of course, that `printservice.config.local.json`
  actually exists — `docker compose up` fails fast naming the missing path
  otherwise. On a native Linux Docker Engine (no Desktop layer),
  `host.docker.internal` needs `extra_hosts: ["host.docker.internal:host-gateway"]`
  added to the compose file, or `CUPS_HOST` pointed at the host's real
  address instead.
- **systemd:** install the file at mode `600`, owned by the `printgateway`
  service user (see the credential-pair paragraph above for why that
  ownership, not `root:root` — `install-services.sh` does this
  automatically) and reference it via `PRINT_GATEWAY_CONFIG` in the unit's
  environment file. Do not bake `PRINT_GATEWAY_CONFIG` into the unit by
  default — a bind-mounted config file that stops being mounted turns a
  healthy service into a startup failure, which argues for it being an
  explicit, deliberate opt-in per deployment rather than a default.
- **Rollback:** because unknown fields are a hard startup error, adding a
  new key to the file and deploying it, then rolling the *binary* back
  without also rolling the file back, makes the older binary refuse to
  start (`unknown field`). Roll forward file-then-binary; roll back
  binary-then-file.
- `tests/scripts/profile.sh` regenerates the test harness's env file from
  `tests/profiles.json` and will drop any hand-added `PRINT_GATEWAY_CONFIG`
  — expected (the harness stays env-only on purpose, see
  `docs/config-file-layer-plan.md`), but worth knowing if `tests/` output
  looks like the config file was ignored.

### Secrets (Vault)

Everything below only matters when `PRINT_GATEWAY_REQUIRE_AUTH=true` — with
it left at its `false` default, no token is resolved at startup and none of
this fallback machinery runs.

Setting `VAULT_ADDR` (Nomad's own injected address variable) or
`SECRET_STORE_URL` switches the print token's source from plain
`PRINT_GATEWAY_TOKEN` to Vault, read at `<LABOS_ENV>/config/print_gateway`,
key `auth-token` (the `LABOS_ENV` prefix is only added when that variable is
set; see `internal/secrets/secrets.go`'s `printTokenPath`).

**This is not the same Vault path the labOS side reads.** The original
intent was for both sides to share one KV entry, but the actual labOS
implementation (`EnvironmentConfigurationOld::GetPdfPrinterParams`, LAB-16894
/ CL 1021434) resolves the gateway host and the token it sends from
`config/pdf_printer` instead — keys `host` and `print-token` — falling back
to the `Autolims.cfg` keys `PDF_PRINTER_ADDRESS` / `PDF_PRINTER_TOKEN` for
whichever of the two Vault keys comes back blank (or if Vault is
unreachable). If the host is still blank after both sources, the PDF-print
resource is left unconfigured and `HtmlPrinter` falls back to the
pre-existing local spooler path unchanged. Until this is reconciled to a
single shared path (or someone deliberately mirrors the token value into
both `config/print_gateway`'s `auth-token` and `config/pdf_printer`'s
`print-token`), a Vault-backed deployment with `PRINT_GATEWAY_REQUIRE_AUTH=true`
needs the same token string seeded under both KV paths for the client's
`X-Labos-Print-Token` header to actually match what this server expects.

| Env var | Meaning |
| :--- | :--- |
| `VAULT_ADDR` | Vault address (the standard Nomad-injected variable). Unset (and `SECRET_STORE_URL` also unset) ⇒ Vault is not used at all; the token comes from `PRINT_GATEWAY_TOKEN` instead, which must then be non-empty — the process refuses to start otherwise (see "Fallback policy" below). |
| `SECRET_STORE_URL` | Overrides `VAULT_ADDR` when both are set — matches `go-packages/settings`' own precedence. |
| `VAULT_TOKEN` | Vault token auth. |
| `SECRET_STORE_USERNAME` / `SECRET_STORE_PASSWORD` | Vault `userpass` auth, used when `VAULT_TOKEN` is empty. `SECRET_STORE_PASSWORD` must be `encryption.Encrypt`-ed, not plaintext — matching the convention `go-packages/settings` already uses for this variable. A password that fails to decrypt is treated as a Vault-init failure (logged, falls back to `PRINT_GATEWAY_TOKEN`). |
| `LABOS_ENV` | Path prefix under the KV mount, e.g. `production`. Unset ⇒ no prefix. |

**Fallback policy, deliberately fail-open (until nothing is left to fall
back to):** when Vault is configured, any failure reading the token — client
construction (bad/missing credentials), an unreachable server, a malformed
response, or the secret genuinely not being present — is logged (never the
token value) and the server falls back to `PRINT_GATEWAY_TOKEN`. The same
rule applies when Vault isn't configured at all: the token then comes
straight from `PRINT_GATEWAY_TOKEN`. Either way, the process refuses to
start only if *neither* Vault nor the environment produced a token — Vault
configured-and-failed with no env token, or Vault not configured at all with
no env token. This is a deliberate choice for this prototype: a
misconfigured or down Vault degrades to env instead of taking the service
down, but "no token anywhere" cannot boot into a server that would 503 every
request forever. It is intentionally more permissive than `secret_store`'s
own `GetSecretStringWithFallback` helper, which only falls back on a
definite miss (see `internal/secrets/secrets.go`).

A present-but-blank Vault value (an unset key, a botched `vault kv put`, a
rotation that cleared it) is treated the same as a miss — it falls back to
`PRINT_GATEWAY_TOKEN` rather than "succeeding" into an empty token, which
would otherwise 503 every request while logging a successful resolution.

The startup log names which source won (`vault`, `env`, or
`env (vault fallback)`) — never the token value itself.

### SSRF defense (`file_url`)

Option 2 above makes the server fetch a caller-supplied URL. Left
unguarded, a caller could point `file_url` at `http://169.254.169.254/` (a
cloud metadata endpoint) or `http://127.0.0.1:631/admin` (this machine's own
CUPS admin interface) and have the response printed on paper. Per HLD §11.3,
every `file_url` fetch is checked before *and* after connecting:

1. Scheme must be `http`/`https`; embedded credentials (`http://user:pass@…`)
   are rejected.
2. Port must be `80` or `443` — this single rule is what kills
   `127.0.0.1:631`.
3. The address actually being connected to — not just the URL string, which
   DNS could resolve differently by the time of the real connect — is
   checked against loopback, private (RFC1918 + `fc00::/7`), link-local
   (`169.254.169.254` included), CGNAT (`100.64.0.0/10`) and a handful of
   other IANA special-purpose IPv4 ranges, multicast, unspecified,
   broadcast, and every IPv6 scheme that embeds an IPv4 address
   (`::/96`, `64:ff9b::/96`, `64:ff9b:1::/48`, `2002::/16`, `2001::/32`,
   `100::/64`) that would otherwise let an IPv4-blocked address through
   under an IPv6-shaped disguise. Enforced in `net.Dialer.Control`, which
   runs after DNS resolution and immediately before `connect(2)` on the
   literal IP — this is what actually stops DNS rebinding, not just the URL
   as originally submitted, and it's sufficient on its own: the address
   `Control` sees is exactly the one `connect(2)` uses, with no re-resolution
   in between. A second, best-effort check re-validates the connection's
   remote address right after it's established and rejects the response
   before any of its body is read — a fail-safe against `Control` itself
   being mis-wired in some future change, not something the primary gate
   depends on.
4. No redirects are followed — a presigned URL is a direct link by
   construction, so any `3xx` response is treated as `file_url must be a
   direct link`.
5. The response is size-bounded (`Content-Length` checked up front, and the
   body always read through a limited reader regardless, so a chunked or
   lying body can't evade the check either).

This guard is a blocklist, not an allowlist — its failure mode is "still
reachable", not "false positive". It does not consult `HTTP_PROXY`/
`HTTPS_PROXY` at all (the `http.Transport`'s `Proxy` field is left `nil`
deliberately): honoring a proxy env var here would let every fetch's real
destination be redirected through the configured proxy, which the address
check would then validate instead of the actual target. In a deployment
that requires an egress proxy for outbound traffic, every `file_url` fetch
will fail — that's a known, accepted limitation of this prototype's
implementation, not a bug.

| Env var | Config file key | Meaning |
| :--- | :--- | :--- |
| `PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS` | *(env-only; setting `allowPrivateTargets` in the file is rejected as an unknown field — see "Configuration file" below)* | `true` disables **every** target check above — address, port, and the post-connect recheck alike, not just the address block. Default `false`; **must stay `false` in any deployment reachable by an untrusted caller.** Exists as a config knob (rather than a test-only code path) because this prototype has no committed tests yet to need it privately; logged at `LogError` (survives any configured log level) when set. |
| `PRINT_GATEWAY_FETCH_ALLOWED_HOSTS` | `resource/printgateway.fetch.allowedHosts` (a JSON array, e.g. `["s3.example.com","cdn.example.com"]`, not a comma-separated string) | Optional host-suffix allowlist. Empty/omitted (the default) means any public host is fetchable — the address block above still applies regardless. In the file, an explicit `[]` means "no allowlist" and suppresses the env var the same way `resource/file_storage.host: ""` does; omitting the key entirely leaves the env var in effect. |
| `PRINT_GATEWAY_FETCH_TIMEOUT` | `resource/printgateway.fetch.timeout` | Bounds a single `file_url` download. Default `60s`. |
| `PRINT_GATEWAY_FETCH_MAX_BYTES` | `resource/printgateway.fetch.maxBytes` | Bounds a downloaded response's size. Default `64` MiB. |

**`s3_key` bypasses all of the above by design** — it talks to a fixed,
configured object-store endpoint with server-side credentials rather than a
caller-supplied URL, so there is no attacker-controlled address to guard
against in the first place. See "S3/MinIO object storage" below.

### S3/MinIO object storage

Two additive capabilities, per HLD §6: fetching a print job by key
(`s3_key` above) and generating presigned URLs (`/files/presign` below).
**Multipart upload (option 1) remains the primary, default intake path and
is never deprecated** — the HLD is explicit that not every Windows caller
has (or should need) an S3 SDK; this is purely for the >10MB / high-volume
cases where it helps.

All the actual S3 logic (auth, presigning, streaming, 404 classification)
lives in the shared `github.com/LabOS-co/go-packages/cloud_storage` package
— `internal/objstore` is a thin adapter onto it, the same role `internal/cups`
and `internal/fetch` play for `lp` and outbound HTTP. See "labOS shared
library" below for that package's status.

Configured by environment and, optionally, by the config file — including
the credential pair, the one deliberate exception to this feature's
"secrets never go in the file" rule; see "Configuration file" below for
that exception's deployment model and file-permission implications. Empty
`PRINT_GATEWAY_S3_ENDPOINT` (the default) disables the feature
entirely — `s3_key` and `/files/presign` both answer `503`, and nothing else
in the service changes. This is **never** a startup failure, unlike the
print token: a misconfigured or absent S3 setup just means the additive
capability isn't available, not that the whole service is down.

| Env var | Config file key | Meaning |
| :--- | :--- | :--- |
| `PRINT_GATEWAY_S3_ENDPOINT` | `resource/file_storage.host` (a bare `host:port`, not a URL — no `https://` prefix; the vendored minio client builds `scheme://` + this value itself, and stripping the scheme is the caller's job) | `host:port` of the S3/MinIO endpoint. Empty ⇒ object storage disabled. Half-configured (this set with `PRINT_GATEWAY_S3_BUCKET` empty, or vice versa) is logged loudly and disables object storage — same as every other broken-S3-config case, **never a startup failure**, matching the invariant stated above. **An explicit `""` in the file suppresses the env value** — how an operator disables object storage from the file rather than editing the deployment's env vars — while *omitting* the key from the file leaves the env var in full effect. This is the one place `""` and "not present" mean different things; do not confuse it with `resource/printgateway.logLevel` below, where `""` is a startup error instead. |
| `PRINT_GATEWAY_S3_BUCKET` | `resource/printgateway.objectStore.bucket` | The one bucket this server reads/writes. There is no per-request bucket override or allowlist (see "deliberately not here yet" below). Same `""`-suppresses-env rule as `resource/file_storage.host`. Note this lives under `resource/printgateway`, NOT `resource/file_storage` — which bucket to use is this service's own concern, the shared block only carries the connection itself (host + credentials). |
| `PRINT_GATEWAY_S3_REGION` | `resource/printgateway.objectStore.region` | Passed through to the S3 client and used to sign every request. **Strongly recommended, not optional in practice**: leaving it empty costs a live network round trip on first use, and — worse — if that lookup itself fails (e.g. against a non-AWS backend that doesn't answer it), the client silently signs as `us-east-1` instead of erroring, so a bad presigned URL fails on whoever tries to use it, with nothing here to trace it back to. Logged at `LogError` when left empty and S3 is otherwise configured. Same `""`-suppresses-env rule as `resource/file_storage.host`. |
| `PRINT_GATEWAY_S3_INSECURE` | `resource/printgateway.objectStore.insecure` | `true` disables TLS to the endpoint (plain `http://`) — for a local/dev MinIO only. Default `false`. |
| `PRINT_GATEWAY_S3_ACCESS_KEY` / `PRINT_GATEWAY_S3_SECRET_KEY` | `resource/file_storage.s3-user` / `resource/file_storage.s3-password` (note: kebab-case JSON keys, and this shared block — not `resource/printgateway.objectStore` — same place `host` lives) — **the one exception to "secrets never go in the file", see "Configuration file" below for the deployment model this is for and the file-permission implications** | Env/file-fallback credentials. Vault is tried first when configured (same Vault-then-env-or-file pattern as the print token — see "Secrets (Vault)" above), at the same path, keys `s3-access-key`/`s3-secret-key`. |
| `PRINT_GATEWAY_S3_TIMEOUT` | `resource/printgateway.objectStore.timeout` | Bounds a single `s3_key` download. Default `60s`. |
| `PRINT_GATEWAY_S3_MAX_BYTES` | `resource/printgateway.objectStore.maxBytes` | Bounds a downloaded object's size — checked against the object store's own authoritative size metadata before any byte is copied, unlike `file_url`'s Content-Length (at best a claim until the read catches a lie). Default `64` MiB. |

**`POST /files/presign`** — same auth (`X-Labos-Print-Token`) as `/print`:

```bash
curl -X POST http://localhost:8090/files/presign \
  -H "X-Labos-Print-Token: <token>" -H "Content-Type: application/json" \
  -d '{"key":"invoices/invoice-42.pdf","method":"GET"}'
# {"url":"http://...(signed)...","key":"invoices/invoice-42.pdf","expires_at":"2026-08-27T15:00:00Z"}
```

`method` is `GET` (default — a URL a third party can fetch, e.g. to hand to
another system that will then print it by `s3_key`) or `PUT` (a URL a third
party can upload to directly, which can then be printed by that same key).
Optional `ttl_seconds` requests a shorter-than-default expiry; a value
*longer* than `PRINT_GATEWAY_PRESIGN_TTL` (default `15m`) is silently
clamped down to it rather than rejected.

`key` is used exactly as given — no server-assigned prefix, no basename
rewriting — except that a key containing a `../`-style traversal segment is
rejected outright with `400` (`s3_key`/`key`, both endpoints), rather than
silently normalized. This is what actually keeps a caller confined to the
one configured bucket: verified live that a real MinIO instance
independently rejects an unclean key server-side too, but that is
backend-specific behavior this guarantee should not rest on alone — the
check is enforced here regardless of backend.

**Deliberately not here yet:** a per-request bucket override or allowlist
(one caller cannot currently be restricted to a sub-prefix of the bucket);
content-type/magic-byte validation of an `s3_key` object, same gap `file_url`
and multipart already have (see "deliberately not here yet" at the bottom);
a concurrency limit on `/print`, so this shares that pre-existing gap too.

### Correlation ID

Every response carries an `X-Laas-Identifier` header — the labOS-wide
correlation id convention. Send one on the request and the server adopts it,
so a print can be traced back to the caller's own transaction; send nothing
and the server generates one. Either way the value comes back on the
response, including on a `401`.

A supplied id is accepted only if it is printable ASCII and at most 128
bytes. Anything else is replaced with a generated id and a log line saying
so — it is never echoed back or written into a log field as given.

The id always reaches the response header. It reaches the *log text* as a
queryable `job_id` field only once logstash shipping is configured (see
"Logging" below) — local console output still prints message text only, not
structured fields, regardless.

Every request also gets exactly one completion line via `logs.LogAPICompletion`
(`job_id`/`duration`/`status`), including a 401 or an oversized-body
rejection, emitted regardless of outcome — even a panic still produces one.
If the panic happens before the handler wrote anything, the client gets a
clean `500` and the logged `status` matches it. If the panic happens after
the handler already started writing a response, the connection is aborted
outright instead — a corrupted-but-parseable `200` is worse than no response
at all — and the logged `status` reflects whatever was already sent before
the panic, not a fabricated `500`.

On local console output, `LogAPICompletion` renders as a message-less,
timestamp-only blank line per request — `logs`' console/JSON formatters both
build the human-readable text from the *message* argument, which this call
never has one of (only structured fields); this is an upstream `logs`
behavior, not something settable from here. The structured fields (and a
real message) only show up once logstash shipping is configured.

```bash
PRINT_GATEWAY_TOKEN='<the shared secret>' ./printgateway-linux-amd64
```

### Logging

The server logs via `logs.GetLoggerWithSettings` (`FormatJSON`), not the
stdlib `log` package or the plain `logs.GetConsoleLogger()` this prototype
started with — see "labOS shared library" below for why. Two things follow
from that:

- **Log level.** `PRINT_GATEWAY_LOG_LEVEL` (default `info`), config file key
  `resource/printgateway.logLevel`, sets the minimum logrus level. An
  invalid value is logged and ignored (falls back to `info`) rather than
  failing startup — logging misconfiguration alone isn't worth refusing to
  serve over. Unlike every other file-sourced string in this document, an
  explicit `""` for `resource/printgateway.logLevel` is a **startup
  error**, not a suppression — `""` means something for
  `resource/file_storage.host`, `resource/printgateway.objectStore.bucket`/
  `region`, and `resource/log`'s host/port (see below and "S3/MinIO object
  storage" above), but a blank log level is meaningless to
  `logger.SetLogLevel`.
- **Shipping to logstash.** Optional and non-fatal: if nothing resolves, the
  server just stays on console-only logging. The address (`host:port`)
  resolves the same way the print token does — Vault first if configured,
  then env, then the config file's `resource/log` block (its `host`/`port`
  are separate JSON string fields, combined into one `"host:port"` value;
  both resolving to `""` suppresses the env value, same suppression
  principle as `resource/file_storage.host`) — and every failure along the
  way is logged once so the degradation is visible:

  | Source | Where |
  | :--- | :--- |
  | Vault | `<LABOS_ENV>/config/print_gateway`, key `log-server` (same path as the print token, different key) |
  | Env fallback | `LOG_SERVER`, e.g. `LOG_SERVER=logstash.internal:514` |
  | Config file | `resource/log`, e.g. `"resource/log": {"host": "logstash.internal", "port": "514"}` |

  The startup log names which source won (`vault` or `env`) — **not yet
  updated to say `file`** when the config file is what actually supplied
  the value; see "Configuration file" below for that known gap.
- **Host identification fields**, both read directly by `go-packages/logs`
  (not by this service's own code, and neither has a config-file key — set
  them however the deployment sets any other plain env var):
  - `HOST_NAME` — set via the systemd unit's `Environment=HOST_NAME=%H`
    specifier (no secret, safe to inline in the unit file itself — see
    `printgateway.service`) or, in Docker/Nomad, typically the container/
    allocation hostname.
  - `HOST_IP` — no systemd specifier equivalent, so it belongs in
    `printgateway.env`/the container's env, not the unit file. Left unset,
    every shipped log record's host-IP field renders as the literal string
    `"NO_VAL"` instead of failing — harmless, but makes per-host filtering
    in Kibana useless.
  - Both are inert until `LOG_SERVER`/Vault's `log-server` resolves to
    something — see above.

**Known limitations, accepted for this prototype:**

- **Console output moved from stdout to stderr.** The old
  `GetConsoleLogger()` printed via `fmt.Print` (stdout); `logrus.New()`
  (what `GetLoggerWithSettings` builds on) defaults its output to stderr.
  A deploy wrapper that only captured stdout needs updating.
- **UDP is fire-and-forget.** `SetLogstashLogger` dials UDP — a successful
  dial proves the address resolved, not that anything is listening on the
  other end. Confirm actual delivery in Kibana (the `kibana-search` skill
  queries by `job_id`), don't infer it from a clean startup log.
- **`environment` is never populated.** `GetLoggerWithSettings` has no
  setter for it (only the `settings`-backed `GetLogger` sets it), so every
  shipped record's `environment` field is empty and dropped by the JSON
  formatter — a Kibana query filtering by environment won't match this
  service at all, even though `LABOS_ENV` is set and used elsewhere (Vault
  path prefixing). This is the concrete reason to revisit `GetLogger()` +
  the `settings` stack (see "labOS shared library" below), not just an
  abstract "nice to have."
- **A log-call sequence counter races under concurrent requests.** The
  `logs` package increments an unsynchronized package-global on every log
  call. Two concurrent failing requests can log through this at the same
  time. Not fixable from this repo — tracked as an upstream `go-packages`
  issue, not this service's bug.

### Timeouts, limits, and shutdown

The server no longer runs with `net/http`'s zero-value timeouts — unset,
a slow or silent client could hold a connection open forever. Each value
below has a default, an env var, and (see "Configuration file" below) a
config-file JSON key that overrides the env var, which in turn overrides
the default. The process refuses to start, naming the offending variable
or JSON key, on any override — from either source — that is unparsable,
non-positive, or inconsistent with the others, rather than silently keeping
the default:

| Setting | Default | Env var | Config file key | Accepted value |
| :--- | :--- | :--- | :--- | :--- |
| Read header timeout | 10s | `PRINT_GATEWAY_READ_HEADER_TIMEOUT` | `resource/printgateway.timeouts.readHeader` | Go duration, positive, `<=` read timeout |
| Read timeout | 5m | `PRINT_GATEWAY_READ_TIMEOUT` | `resource/printgateway.timeouts.read` | Go duration, positive |
| Write timeout | 8m | `PRINT_GATEWAY_WRITE_TIMEOUT` | `resource/printgateway.timeouts.write` | Go duration, positive, **`>` read timeout + max(fetch timeout, S3 timeout) + submit timeout** |
| Idle timeout | 60s | `PRINT_GATEWAY_IDLE_TIMEOUT` | `resource/printgateway.timeouts.idle` | Go duration, positive |
| Max header bytes | 64 KiB | `PRINT_GATEWAY_MAX_HEADER_BYTES` | `resource/printgateway.limits.maxHeaderBytes` | plain integer **number of bytes** (`65536`, not `64KiB`) |
| Shutdown grace period | 2m | `PRINT_GATEWAY_SHUTDOWN_GRACE` | `resource/printgateway.timeouts.shutdownGrace` | Go duration, positive, **`>` max(fetch timeout, S3 timeout) + submit timeout** |
| Submit (`lp`) timeout | 30s | `PRINT_GATEWAY_SUBMIT_TIMEOUT` | `resource/printgateway.timeouts.submit` | Go duration, positive |
| Fetch (`file_url`) timeout | 60s | `PRINT_GATEWAY_FETCH_TIMEOUT` | `resource/printgateway.fetch.timeout` | Go duration, positive |
| Fetch (`file_url`) max size | 64 MiB | `PRINT_GATEWAY_FETCH_MAX_BYTES` | `resource/printgateway.fetch.maxBytes` | plain integer **number of bytes** |
| S3 (`s3_key`) timeout | 60s | `PRINT_GATEWAY_S3_TIMEOUT` | `resource/printgateway.objectStore.timeout` | Go duration, positive |
| S3 (`s3_key`) max size | 64 MiB | `PRINT_GATEWAY_S3_MAX_BYTES` | `resource/printgateway.objectStore.maxBytes` | plain integer **number of bytes** |
| Presign expiry (default and cap) | 15m | `PRINT_GATEWAY_PRESIGN_TTL` | `resource/printgateway.objectStore.presignTtl` | Go duration, positive |
| Max upload (`multipart/form-data`) body | 64 MiB | `PRINT_GATEWAY_MAX_UPLOAD_BYTES` | `resource/printgateway.limits.maxUploadBytes` | plain integer **number of bytes** |
| Max JSON body (`application/json`, incl. `/files/presign`) | 8 KiB | `PRINT_GATEWAY_MAX_JSON_BYTES` | `resource/printgateway.limits.maxJsonBytes` | plain integer **number of bytes** |

**One surprising interaction:** the env var behind a setting is still
validated even when the config file supersedes it — a malformed
`PRINT_GATEWAY_WRITE_TIMEOUT` still fails startup even if
`resource/printgateway.timeouts.write` in the file is perfectly valid and would have won anyway.
This is deliberate (see `mergeFileConfig`'s doc comment in
`internal/config/config.go`), not an oversight: it means a stale or
fat-fingered env var left behind after migrating a setting into the file is
caught immediately instead of resurfacing silently the day the file is
removed.

Zero and negative durations are rejected on purpose: `net/http` guards every
timeout with `if d > 0`, so `0` or `-5s` does not mean "very short", it means
*no timeout at all* — a typo would silently restore the exposure these values
exist to close.

**Why the write timeout is the largest value.** `net/http` arms the write
deadline when the request *headers* are parsed, not when the response starts
(`conn.readRequest` sets it in a `defer`). So it is the budget for reading the
body, spooling it, downloading `file_url`/`s3_key`, running `lp`, *and*
sending the response. Set too low, a slow request is accepted, fetched, and
printed, and then fails on the response write — the caller sees a failure
for a job that actually succeeded, retries, and the document prints twice.
Startup enforces `write timeout > read timeout + max(fetch timeout, S3
timeout) + submit timeout` — the *max*, not the sum, of the two download
timeouts, since a single request only ever exercises one of `file_url`/
`s3_key`, never both (the JSON intake rejects a request naming both). Summing
them instead would have tightened this budget — and the shutdown-grace one
below it — for every deployment the moment `PRINT_GATEWAY_S3_TIMEOUT` got a
default, whether or not object storage is even configured.

Every request body is also bounded before it is read, regardless of which
route or auth outcome follows: `multipart/form-data` up to the max-upload
size, everything else (the JSON print-by-reference intake, `/files/presign`)
up to the much smaller max-JSON size. This is wired above authentication —
`http.MaxBytesReader` is lazy, so wrapping the body early costs nothing and
makes "the body is bounded" true of the whole request handling stack, not
just of whichever handler happens to parse it.

On `SIGINT`/`SIGTERM` the server stops accepting new connections and waits
up to the shutdown grace period for in-flight requests to finish before the
process exits — a print already spooling is allowed to complete rather than
being cut off mid-upload.

Every operation a handler can block on now has a real, ctx-bounded timeout —
`cups.LPSubmitter` uses `exec.CommandContext` (submit timeout),
`fetch.SafeFetcher` is dialed and read through a context `printgw.Service`
bounds to the fetch timeout, and `objstore.MinIO`'s `Get` is bounded to the
S3 timeout the same way — so a wedged CUPS queue, an unresponsive `file_url`
host, or a stalled S3 endpoint fails the request instead of holding it, and
the shutdown grace period, for the full duration. `config.Load` asserts
`ShutdownGrace > max(FetchTimeout, S3Timeout) + SubmitTimeout` so a request
already at that budget still has room to finish draining rather than being
cut off by `Shutdown` itself.

`net/http`'s own error lines (e.g. a client that tripped the read header
timeout) are routed through the same logger as every request instead of
`net/http`'s default stderr logger. They carry no `X-Laas-Identifier`
correlation id — `net/http` raises them below the layer where a request
context exists, so there is nothing to correlate them to; only the
per-request lines from the handler chain are traceable by id.

## Building and running

This needs to run where CUPS is, i.e. inside the WSL Ubuntu install (see
`docs/STATUS.md` for how to get that environment up). Build for Linux from
Windows and copy the resulting binary over, or build directly inside WSL:

`-ldflags` populates `/status`'s `version`/`build`/`label` fields (see
"Health check" above) by setting `github.com/version-go/ldflags`'s otherwise-
`"unknown"` package vars — omit it (as the plain commands below do) and
`/status` still works, just reporting `"unknown"` for all three:

```bash
LDFLAGS="-X github.com/version-go/ldflags.buildVersion=$(git describe --tags --always) \
  -X github.com/version-go/ldflags.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -X github.com/version-go/ldflags.buildHash=$(git rev-parse --short HEAD)"
```

```bash
# from Windows (cross-compile):
cd src/printgateway
GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o printgateway-linux-amd64 ./cmd/printgateway
# copy printgateway-linux-amd64 into the WSL filesystem, then inside WSL:
chmod +x printgateway-linux-amd64
./printgateway-linux-amd64                    # listens on 0.0.0.0:8090
PORT=9000 ./printgateway-linux-amd64          # or pick a different port
PRINT_GATEWAY_BIND_HOST=127.0.0.1 ./printgateway-linux-amd64  # loopback-only, for a local test
```

```bash
# or, directly inside WSL, if a Go toolchain is installed there:
cd src/printgateway
go build -ldflags "$LDFLAGS" -o printgateway ./cmd/printgateway
./printgateway
```

## labOS shared library

This service depends on six packages from the shared
[`github.com/LabOS-co/go-packages`](https://github.com/LabOS-co/go-packages)
monorepo (each package there is its own Go module, versioned with its own
`<package>/vX.Y.Z` git tags):

- `github.com/LabOS-co/go-packages/logs` — used via
  `logs.GetLoggerWithSettings(logs.LogsSettings{Format: logs.FormatJSON}, ...)`
  for every log line, instead of the stdlib `log` package or the plain
  `logs.GetConsoleLogger()` this prototype started with. Constructed
  *without* a `Host`, then `logger.SetLogstashLogger(host, port)` is called
  separately once `internal/secrets.ResolveLogServer` resolves one — because
  `GetLoggerWithSettings`'s own internal call to that same function
  discards its error and reports success regardless (`logs@v1.5.2/logs.go`),
  which would hide a real dial failure. See "Logging" above for the
  env/Vault knobs and known limitations.
- `github.com/LabOS-co/go-packages/error_handler` — every non-2xx response is
  built with `error_handler.NewErrorHandler(logger, metaData).HandleError(...)`,
  so failures come back in the same JSON envelope (`errorCode`/`errorDetails`/
  `errorMessage`) as every other labOS Go service, and every failure is logged
  automatically as it's handled.
- `github.com/LabOS-co/go-packages/secret_store` — `internal/secrets` uses its
  `Vault(...)` client plus `GetSecretString` to resolve the print token,
  logstash address, and S3 credentials when `SECRET_STORE_URL` is set (see
  "Secrets (Vault)" above). **Not yet on a tagged release**: the two
  functions this depends on (`GetSecretString`/`GetSecretStringWithFallback`)
  live on the unpushed branch
  `feature/secret_store/LAB-16894—Add_secret_fallback_helpers` in the
  `go-packages` repo, checked out into a separate **git worktree** (not the
  main `go-packages` checkout — see the `cloud_storage` entry below for why),
  so `go.mod` currently carries a local
  `replace github.com/LabOS-co/go-packages/secret_store =>
  ../../../go-packages-secret_store-wt/secret_store` pointing at it. Remove
  the `replace` and bump the `require` to a real tag once that branch is
  merged and tagged.
- `github.com/LabOS-co/go-packages/encryption` — `internal/secrets` uses
  `Decrypt` on `SECRET_STORE_PASSWORD`, matching the convention
  `go-packages/settings` already applies to that variable (see the table
  above). A real tagged release (`v1.1.1`), no local `replace` needed.
- `github.com/LabOS-co/go-packages/cloud_storage` — `internal/objstore`
  adapts its `CloudStorageStreamingClient` (presigning + ctx-cancellable
  streaming Get/Put) to this service's own `printgw.ObjectStore` port (see
  "S3/MinIO object storage" above). **Not yet on a tagged release**: those
  methods live on the unpushed branch
  `feature/cloud_storage/LAB-16894—Add_presign_and_streaming_support` in the
  `go-packages` repo — currently the *main* `go-packages` checkout (which is
  why `secret_store`, above, needed its own separate worktree instead: one
  working directory can only be on one branch at a time), so `go.mod` carries
  `replace github.com/LabOS-co/go-packages/cloud_storage =>
  ../../../go-packages/cloud_storage`. Remove the `replace` and bump the
  `require` to a real tag once that branch is merged and tagged — and note
  that whichever of these two branches merges first should let the other
  drop its worktree and rejoin the main checkout.
- `github.com/LabOS-co/go-packages/system_api` — `internal/httpapi.NewServer`
  calls `system_api.Status` directly to mount `GET /status`; `main.go`'s
  `run()` separately calls `system_api.Register` (see "Health check" above)
  purely for its opt-in Consul self-registration, once the listener is
  confirmed up. Real tagged release, `v0.0.10`, no local `replace` needed —
  but its own `go.mod` under-declares its `system_args` dependency at
  `v0.0.5`, which lacks the `ShouldRegisterToConsul` function this package
  actually calls; `go.mod` here pins `system_args` to `v0.0.11` explicitly
  (`go mod tidy` will not remove this override on its own, but don't delete
  it by hand either — the build breaks at `v0.0.5`).

`logs.GetLogger()` (which resolves its logstash host/port from a full labOS
`settings`-backed setup) is still not used, deliberately: this standalone WSL
prototype doesn't have that stack, and `internal/secrets.ResolveLogServer`
gets the same Vault-then-env result without it. The concrete reason to
revisit that choice is the `environment` field gap noted under "Logging"
above — `GetLoggerWithSettings` has no way to populate it, `GetLogger` does.

To fetch or update these packages, `GOPRIVATE=github.com/LabOS-co` must be
set (this repo's `go.mod`/`go.sum` already pin working versions, so a normal
`go build` doesn't need it — only `go get -u .../logs` etc. does):

```bash
go env -w GOPRIVATE=github.com/LabOS-co
go get github.com/LabOS-co/go-packages/logs@latest
```

## What's deliberately not here yet

This prototype intentionally skips almost everything in
`print-gateway-hld-phase1.docx` — it exists to prove the core mechanism
works, not to be run in production:

- **No queue** — a request either prints or fails right now, synchronously.
  The doc's still-open decision (direct request vs. queue, section 4.1)
  isn't resolved here either way.
- **No idempotency key / retry / DLQ** (section 9-10) — if `lp` fails, the
  caller finds out immediately and has to decide what to do.
- **No audit trail** (section 8) — nothing is persisted; the log line per
  request is all there is. Correlation IDs *are* assigned and, once
  logstash shipping is configured, do reach the log text as a queryable
  `job_id` field (see "Correlation ID" and "Logging" above) — that's a log
  line, not an audit record.
- **No concurrency limit on `/print`.** An authenticated caller can trigger
  unlimited concurrent `file_url` fetches (or `s3_key` downloads) —
  `file_url` is usable as a reflector against a third party, and either path
  can write up to its configured max size to disk before the size check
  rejects it.
- **No content validation of a fetched or uploaded document.** No intake
  option checks `Content-Type`, a PDF magic number, or a minimum size — a
  non-PDF body is spooled and handed to `lp` as a success (`GW-MP-10`/
  `GW-S3-10` in `tests/TEST-PLAN.md` assert exactly that 200). A *zero-byte*
  body is the one case that does not get through: nothing here rejects it
  either, but `lp` itself then fails with "No file in print request", which
  surfaces as the generic 500 (`GW-MP-11`/`GW-S3-11`). The gap is real; its
  blast radius is one case smaller than it looks.
- **No per-caller bucket restriction.** `s3_key` and `/files/presign` always
  operate against the one bucket configured at startup — there is no
  allowlist or per-request bucket override, so every authenticated caller
  can read/write anywhere in that bucket.
- **No hot-reload of the config file** (see "Configuration file" below) — a
  change to `printservice.config.json` (or whatever `PRINT_GATEWAY_CONFIG`
  names) takes effect only on the next restart, same as every env var.
- **No JSON Schema validation of the config file.** Strict decoding rejects
  an unknown field and a wrong JSON type, but field-name matching stays
  case-insensitive (`"MaxUploadBytes"` is accepted, not just `"maxUploadBytes"`)
  and a duplicate group key merges rather than erroring — see "Configuration
  file" below.

Treat this as the "does the plumbing work at all" step, not a deployable
service.
