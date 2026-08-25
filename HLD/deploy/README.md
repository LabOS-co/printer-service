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
   `X-Labos-Print-Token`, matched in constant time against the
   `PRINT_GATEWAY_TOKEN` environment variable. A missing or wrong token is
   `401`, and it is logged with the caller's address. If
   `PRINT_GATEWAY_TOKEN` is not set the server answers `503` to everything
   rather than serving unauthenticated — it will not silently run open.

The token never appears in this repository or in the labOS source. On the
labOS side it comes from `gSecretManager` (`config/print_gateway`,
key `auth-token`); here it comes from the environment.

### Correlation ID

Every response carries an `X-Laas-Identifier` header — the labOS-wide
correlation id convention. Send one on the request and the server adopts it,
so a print can be traced back to the caller's own transaction; send nothing
and the server generates one. Either way the value comes back on the
response, including on a `401`.

A supplied id is accepted only if it is printable ASCII and at most 128
bytes. Anything else is replaced with a generated id and a log line saying
so — it is never echoed back or written into a log field as given.

Note the id is currently visible in the response header only, not in the
server's log text: the prototype logs via `logs.GetConsoleLogger()`, whose
methods ignore log metadata. Attaching the logstash logger is what turns the
id into a queryable `job_id` field.

```bash
PRINT_GATEWAY_TOKEN='<the shared secret>' ./printgateway-linux-amd64
```

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

This service depends on two packages from the shared
[`github.com/LabOS-co/go-packages`](https://github.com/LabOS-co/go-packages)
monorepo (each package there is its own Go module, versioned with its own
`<package>/vX.Y.Z` git tags):

- `github.com/LabOS-co/go-packages/logs` — used via `logs.GetConsoleLogger()`
  for every log line, instead of the stdlib `log` package.
- `github.com/LabOS-co/go-packages/error_handler` — every non-2xx response is
  built with `error_handler.NewErrorHandler(logger, metaData).HandleError(...)`,
  so failures come back in the same JSON envelope (`errorCode`/`errorDetails`/
  `errorMessage`) as every other labOS Go service, and every failure is logged
  automatically as it's handled.

`GetConsoleLogger()` was chosen over `logs.GetLogger()` deliberately: the
latter needs a full labOS `settings` (Consul-backed) + Vault `secret_store`
setup to resolve its own logstash host/port, which this standalone WSL
prototype doesn't have. Switch to `logs.GetLogger()` (and consider sourcing
`PRINT_GATEWAY_TOKEN` from `secret_store` instead of the environment, matching
how the labOS side already reads it from `gSecretManager`) once this runs
alongside the rest of the labOS infrastructure rather than standalone in WSL.

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
  request is all there is. Correlation IDs *are* now assigned (see
  "Correlation ID" above), but they are not yet visible in the log text,
  because the console logger discards log metadata — that arrives with the
  logstash logger.
- **No SSRF protection on `file_url`** (section 11.3) — the server will
  fetch whatever URL it's given, with no allowlist or internal-IP
  blocking yet. Fine for testing, not for anything reachable by an
  untrusted caller.
- **No status endpoint** (section 5) — the HTTP response is the only
  feedback you get.

Treat this as the "does the plumbing work at all" step, not a deployable
service.
