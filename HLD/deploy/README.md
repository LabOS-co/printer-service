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

A request body over the configured size limit (see "Timeouts, limits, and
shutdown" below) gets `413`, naming the limit in `errorDetails.details`,
rather than the generic `400` every other malformed-body case gets — the two
are otherwise indistinguishable from the response alone.

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

| Env var | Meaning |
| :--- | :--- |
| `PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS` | `true` disables **every** target check above — address, port, and the post-connect recheck alike, not just the address block. Default `false`; **must stay `false` in any deployment reachable by an untrusted caller.** Exists as a config knob (rather than a test-only code path) because this prototype has no committed tests yet to need it privately; logged at `LogError` (survives any configured log level) when set. |
| `PRINT_GATEWAY_FETCH_ALLOWED_HOSTS` | Optional comma-separated host-suffix allowlist (e.g. `s3.example.com,cdn.example.com`). Empty (the default) means any public host is fetchable — the address block above still applies regardless. |
| `PRINT_GATEWAY_FETCH_TIMEOUT` | Bounds a single `file_url` download. Default `60s`. |
| `PRINT_GATEWAY_FETCH_MAX_BYTES` | Bounds a downloaded response's size. Default `64` MiB. |

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

Configured entirely by environment; empty `PRINT_GATEWAY_S3_ENDPOINT` (the
default) disables the feature entirely — `s3_key` and `/files/presign` both
answer `503`, and nothing else in the service changes. This is **never** a
startup failure, unlike the print token: a misconfigured or absent S3 setup
just means the additive capability isn't available, not that the whole
service is down.

| Env var | Meaning |
| :--- | :--- |
| `PRINT_GATEWAY_S3_ENDPOINT` | `host:port` of the S3/MinIO endpoint. Empty ⇒ object storage disabled. Half-configured (this set with `PRINT_GATEWAY_S3_BUCKET` empty, or vice versa) is logged loudly and disables object storage — same as every other broken-S3-config case, **never a startup failure**, matching the invariant stated above. |
| `PRINT_GATEWAY_S3_BUCKET` | The one bucket this server reads/writes. There is no per-request bucket override or allowlist (see "deliberately not here yet" below). |
| `PRINT_GATEWAY_S3_REGION` | Passed through to the S3 client and used to sign every request. **Strongly recommended, not optional in practice**: leaving it empty costs a live network round trip on first use, and — worse — if that lookup itself fails (e.g. against a non-AWS backend that doesn't answer it), the client silently signs as `us-east-1` instead of erroring, so a bad presigned URL fails on whoever tries to use it, with nothing here to trace it back to. Logged at `LogError` when left empty and S3 is otherwise configured. |
| `PRINT_GATEWAY_S3_INSECURE` | `true` disables TLS to the endpoint (plain `http://`) — for a local/dev MinIO only. Default `false`. |
| `PRINT_GATEWAY_S3_ACCESS_KEY` / `PRINT_GATEWAY_S3_SECRET_KEY` | Env-fallback credentials. Vault is tried first when configured (same Vault-then-env pattern as the print token — see "Secrets (Vault)" above), at the same path, keys `s3-access-key`/`s3-secret-key`. |
| `PRINT_GATEWAY_S3_TIMEOUT` | Bounds a single `s3_key` download. Default `60s`. |
| `PRINT_GATEWAY_S3_MAX_BYTES` | Bounds a downloaded object's size — checked against the object store's own authoritative size metadata before any byte is copied, unlike `file_url`'s Content-Length (at best a claim until the read catches a lie). Default `64` MiB. |

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
| Write timeout | 8m | `PRINT_GATEWAY_WRITE_TIMEOUT` | Go duration, positive, **`>` read timeout + max(fetch timeout, S3 timeout) + submit timeout** |
| Idle timeout | 60s | `PRINT_GATEWAY_IDLE_TIMEOUT` | Go duration, positive |
| Max header bytes | 64 KiB | `PRINT_GATEWAY_MAX_HEADER_BYTES` | plain integer **number of bytes** (`65536`, not `64KiB`) |
| Shutdown grace period | 2m | `PRINT_GATEWAY_SHUTDOWN_GRACE` | Go duration, positive, **`>` max(fetch timeout, S3 timeout) + submit timeout** |
| Submit (`lp`) timeout | 30s | `PRINT_GATEWAY_SUBMIT_TIMEOUT` | Go duration, positive |
| Fetch (`file_url`) timeout | 60s | `PRINT_GATEWAY_FETCH_TIMEOUT` | Go duration, positive |
| Fetch (`file_url`) max size | 64 MiB | `PRINT_GATEWAY_FETCH_MAX_BYTES` | plain integer **number of bytes** |
| S3 (`s3_key`) timeout | 60s | `PRINT_GATEWAY_S3_TIMEOUT` | Go duration, positive |
| S3 (`s3_key`) max size | 64 MiB | `PRINT_GATEWAY_S3_MAX_BYTES` | plain integer **number of bytes** |
| Presign expiry (default and cap) | 15m | `PRINT_GATEWAY_PRESIGN_TTL` | Go duration, positive |
| Max upload (`multipart/form-data`) body | 64 MiB | `PRINT_GATEWAY_MAX_UPLOAD_BYTES` | plain integer **number of bytes** |
| Max JSON body (`application/json`, incl. `/files/presign`) | 8 KiB | `PRINT_GATEWAY_MAX_JSON_BYTES` | plain integer **number of bytes** |

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

This service depends on five packages from the shared
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
- **No status endpoint** (section 5) — the HTTP response is the only
  feedback you get.
- **No concurrency limit on `/print`.** An authenticated caller can trigger
  unlimited concurrent `file_url` fetches (or `s3_key` downloads) —
  `file_url` is usable as a reflector against a third party, and either path
  can write up to its configured max size to disk before the size check
  rejects it.
- **No content validation of a fetched or uploaded document.** No intake
  option checks `Content-Type`, a PDF magic number, or a minimum size — a
  200 response with an empty or non-PDF body is spooled and handed to `lp`
  as a success.
- **No per-caller bucket restriction.** `s3_key` and `/files/presign` always
  operate against the one bucket configured at startup — there is no
  allowlist or per-request bucket override, so every authenticated caller
  can read/write anywhere in that bucket.

Treat this as the "does the plumbing work at all" step, not a deployable
service.
