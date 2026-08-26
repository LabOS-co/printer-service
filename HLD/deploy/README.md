# Print Gateway — initial prototype

## What this is

A minimal HTTP server that accepts a print request — a printer name and a
file path — and prints it. It's a prototype meant to prove the basic
request-in / paper-out path end to end, not the production Gateway
described in `print-gateway-hld-phase1.docx`. It has none of that
document's HA, queue, Audit, retry, or security layers yet.

## How it prints

It does the simplest thing that's consistent with everything already
validated in this project: it shells out to CUPS's own `lp` command —

```
lp -d <printer-name> <path>
```

`<printer-name>` must already exist as a CUPS queue (`lpadmin`-configured,
with a static PPD, and behind `ippfix` if that specific printer needs it —
exactly the setup already built and tested in `WSL/setup-15-queues.sh` /
`WSL/setup-cups-queues-for-emulators.sh`). This server does not talk IPP
itself, does not know about PPDs/media/resolution, and does not decide
whether a printer needs `ippfix` — CUPS's own queue configuration handles
all of that already. If you want to print to a printer that doesn't have a
queue yet, set one up the same way the existing ones were set up, then use
that queue's name here.

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

Both `printer` (the CUPS queue name — run `lpstat -p` to see what's
available) are required either way.

Success response (either option):

```json
{"status":"submitted","output":"request id is q-hp-laserjet-42 (1 file(s))\n"}
```

Failure returns a non-2xx HTTP status with a labOS-standard error envelope
(same shape every other labOS Go service returns, via
`github.com/LabOS-co/go-packages/error_handler` — see below):

```json
{"errorCode":"21","errorDetails":{"details":"missing url parameter: printer"},"errorMessage":"Internal server error"}
```

The failure detail is in `errorDetails.details`; check the server's own log
output too — every request (success or failure) is logged there as well.

## Access control

The server is closed to anything that is not the calling system, in two
independent layers — the same model `HtmlToPdf` relies on, plus a token:

1. **Listen address.** It binds `127.0.0.1:8090` by default, so it is not
   reachable from any other machine at all. Passing an address argument
   overrides this — use a specific internal interface, never `:8090`, which
   would listen on every interface including any external one.
2. **Shared token.** Every request must carry the header
   `X-Labos-Print-Token`, matched in constant time against the resolved print
   token (`PRINT_GATEWAY_TOKEN`, or Vault — see "Secrets (Vault)" below). A
   missing or wrong token is `401`, and it is logged with the caller's
   address. If no token can be resolved from any configured source, the
   process refuses to start rather than serving unauthenticated or answering
   `503` to everything forever — see "Fallback policy" below.

The token never appears in this repository or in the labOS source. On the
labOS side it comes from `gSecretManager` (`config/print_gateway`,
key `auth-token`); here it comes from the environment, or from Vault when
configured — see "Secrets (Vault)" below.

### Secrets (Vault)

Setting `VAULT_ADDR` (Nomad's own injected address variable) or
`SECRET_STORE_URL` switches the print token's source from plain
`PRINT_GATEWAY_TOKEN` to Vault, read at the same path/key the labOS side
already uses (`<LABOS_ENV>/config/print_gateway`, key `auth-token` — the
`LABOS_ENV` prefix is only added when that variable is set). This mirrors
`gSecretManager`'s convention deliberately, so a Vault-backed deployment
needs no new path convention on either side.

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

```bash
PRINT_GATEWAY_TOKEN='<the shared secret>' ./printgateway-linux-amd64
```

### Logging

The server logs via `logs.GetLoggerWithSettings` (`FormatJSON`), not the
stdlib `log` package or the plain `logs.GetConsoleLogger()` this prototype
started with — see "labOS shared library" below for why. Two things follow
from that:

- **Log level.** `PRINT_GATEWAY_LOG_LEVEL` (default `info`) sets the
  minimum logrus level. An invalid value is logged and ignored (falls back
  to `info`) rather than failing startup — logging misconfiguration alone
  isn't worth refusing to serve over.
- **Shipping to logstash.** Optional and non-fatal: if nothing resolves, the
  server just stays on console-only logging. The address (`host:port`)
  resolves the same way the print token does — Vault first if configured,
  then env — and every failure along the way is logged once so the
  degradation is visible:

  | Source | Where |
  | :--- | :--- |
  | Vault | `<LABOS_ENV>/config/print_gateway`, key `log-server` (same path as the print token, different key) |
  | Env fallback | `LOG_SERVER`, e.g. `LOG_SERVER=logstash.internal:514` |

  The startup log names which source won (`vault` or `env`), mirroring the
  print token's own `vault`/`env` source log line.

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
below has a default and an env var to override it without a rebuild. The
process refuses to start, naming the offending variable, on an override
that is unparsable, non-positive, or inconsistent with the others — rather
than silently keeping the default:

| Setting | Default | Env var | Accepted value |
| :--- | :--- | :--- | :--- |
| Read header timeout | 10s | `PRINT_GATEWAY_READ_HEADER_TIMEOUT` | Go duration, positive, `<=` read timeout |
| Read timeout | 5m | `PRINT_GATEWAY_READ_TIMEOUT` | Go duration, positive |
| Write timeout | 6m | `PRINT_GATEWAY_WRITE_TIMEOUT` | Go duration, positive, **`>` read timeout** |
| Idle timeout | 60s | `PRINT_GATEWAY_IDLE_TIMEOUT` | Go duration, positive |
| Max header bytes | 64 KiB | `PRINT_GATEWAY_MAX_HEADER_BYTES` | plain integer **number of bytes** (`65536`, not `64KiB`) |
| Shutdown grace period | 2m | `PRINT_GATEWAY_SHUTDOWN_GRACE` | Go duration, positive |

Zero and negative durations are rejected on purpose: `net/http` guards every
timeout with `if d > 0`, so `0` or `-5s` does not mean "very short", it means
*no timeout at all* — a typo would silently restore the exposure these values
exist to close.

**Why the write timeout is the largest value.** `net/http` arms the write
deadline when the request *headers* are parsed, not when the response starts
(`conn.readRequest` sets it in a `defer`). So it is the budget for reading the
body, spooling it, downloading `file_url`, running `lp`, *and* sending the
response. Set below the read timeout, a slow upload is accepted and printed
and then fails on the response write — the caller sees a failure for a job
that actually succeeded, retries, and the document prints twice. Startup
enforces `write timeout > read timeout` for that reason.

On `SIGINT`/`SIGTERM` the server stops accepting new connections and waits
up to the shutdown grace period for in-flight requests to finish before the
process exits — a print already spooling is allowed to complete rather than
being cut off mid-upload.

> **Caveat until per-operation timeouts land.** `Shutdown` can only wait for
> handlers; it cannot interrupt them. `fetch.HTTPFetcher` still uses
> `http.Get` with no timeout and ignores its context, and `cups.LPSubmitter`
> still uses `exec.Command` rather than `exec.CommandContext`. A handler
> blocked on an unresponsive `file_url` host therefore cannot be drained: it
> holds the full grace period, `Shutdown` returns `DeadlineExceeded`, the
> process exits non-zero, and that request's temp spool file is left behind.
> Wiring `FetchTimeout`/`SubmitTimeout` to real cancellation is what closes
> this.

`net/http`'s own error lines (e.g. a client that tripped the read header
timeout) are routed through the same logger as every request instead of
`net/http`'s default stderr logger. They carry no `X-Laas-Identifier`
correlation id — `net/http` raises them below the layer where a request
context exists, so there is nothing to correlate them to; only the
per-request lines from the handler chain are traceable by id.

## Building and running

This needs to run where CUPS is, i.e. inside the WSL Ubuntu install (see
`HDL/STATUS.md` for how to get that environment up). Build for Linux from
Windows and copy the resulting binary over, or build directly inside WSL:

```bash
# from Windows (cross-compile):
cd HDL/deploy
GOOS=linux GOARCH=amd64 go build -o printgateway-linux-amd64 .
# copy printgateway-linux-amd64 into the WSL filesystem, then inside WSL:
chmod +x printgateway-linux-amd64
./printgateway-linux-amd64            # listens on :8090
./printgateway-linux-amd64 :9000      # or pick a different port
```

```bash
# or, directly inside WSL, if a Go toolchain is installed there:
cd deploy
go build -o printgateway .
./printgateway
```

## labOS shared library

This service depends on four packages from the shared
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
  `Vault(...)` client plus `GetSecretString` to resolve the print token when
  `SECRET_STORE_URL` is set (see "Secrets (Vault)" above). **Not yet on a
  tagged release**: the two functions this depends on
  (`GetSecretString`/`GetSecretStringWithFallback`) live on the unpushed
  branch `feature/secret_store/LAB-16894—Add_secret_fallback_helpers` in the
  `go-packages` repo, so `go.mod` currently carries a local
  `replace github.com/LabOS-co/go-packages/secret_store =>
  ../../../go-packages/secret_store` pointing at that clone. Remove the
  `replace` and bump the `require` to a real tag once that branch is merged
  and tagged.
- `github.com/LabOS-co/go-packages/encryption` — `internal/secrets` uses
  `Decrypt` on `SECRET_STORE_PASSWORD`, matching the convention
  `go-packages/settings` already applies to that variable (see the table
  above). A real tagged release (`v1.1.1`), no local `replace` needed.

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
- **No SSRF protection on `file_url`** (section 11.3) — the server will
  fetch whatever URL it's given, with no allowlist or internal-IP
  blocking yet. Fine for testing, not for anything reachable by an
  untrusted caller.
- **No status endpoint** (section 5) — the HTTP response is the only
  feedback you get.

Treat this as the "does the plumbing work at all" step, not a deployable
service.
