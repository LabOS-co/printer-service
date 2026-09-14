# LAB-16894 Print Server — system test plan

Covers every externally-observable behavior of the three shipped components
(`printgateway`, `printersearch`, `ippfix`) as **system tests against running
processes** — not unit tests. `src/printgateway` already has a near-100%
statement-coverage Go unit suite (`go test ./...`); this plan deliberately does
not restate what that suite proves in-process. What it adds is everything a unit
test structurally cannot reach: real CUPS, real paper, a real Vault, a real
object store (three of them), a real network, real process startup/shutdown, and
the interaction between them.

- **Scenario matrix** — §5 onward, one table per functional area.
- **How to actually run it** — §4 (profiles) and §11 (runbooks).
- **Automation** — `tests/postman/` (HTTP, via Postman or Newman) and
  `tests/scripts/ipp-suite.sh` (IPP/CLI, which Postman cannot reach).

---

## 1. Components under test

| Component | Path | Interface | Driven by |
| :--- | :--- | :--- | :--- |
| Print Gateway | `src/printgateway` | HTTP (`POST /print`, `POST /files/presign`) | Postman / Newman |
| printersearch | `src/printersearch` | CLI over raw IPP | `scripts/ipp-suite.sh` |
| ippfix | `src/ippfix` | IPP reverse proxy | `scripts/ipp-suite.sh` |
| win-bench | `src/win-bench` | PowerShell + SumatraPDF | Out of scope — control arm for benchmarks, not a deliverable |

## 2. What this plan does **not** cover

Stated explicitly so nobody writes a scenario for something unreachable from
outside the process:

- **Panic recovery** (`panicRecovery` middleware) — no external input can make a
  handler panic; the only coverage is `internal/httpapi/middleware_test.go`.
- **`LogMetaData` race safety** — needs `-race`, which is a unit-test concern
  (and cannot run natively on this Windows box; run it in WSL where gcc exists).
- **Vault client internals** (`encryption.Decrypt`, `secret_store` wiring) —
  covered in `internal/secrets`. This plan tests the *observable outcome*: which
  source won, and whether the resulting token authenticates.
- **`error_handler` envelope shape** — see the assertion rule in §3.

## 3. Assertion rules (read before writing any test)

1. **Never assert on `errorCode` or `errorMessage`.** `error_handler@v1.2.4`
   hardcodes `"errorCode":"21"` and `"errorMessage":"Internal server error"` on
   **every** failure regardless of status — a 400, a 401 and a 502 are
   byte-identical in those two fields. The only discriminating parts of a failure
   response are the **HTTP status** and **`errorDetails.details`**.
2. **Assert `errorDetails.details` by substring, not equality.** Several messages
   interpolate a Go error (`invalid JSON body: <err>`) whose text belongs to the
   stdlib and may change across Go versions.
3. **Every response, including 401, carries `X-Laas-Identifier`.** Assert its
   presence globally, in a collection-level test.
4. **A 500 must never contain a path, a `lp` stderr line, or a URL.** The
   `apperr.Public`/`Internal` split is a security property; assert the *absence*
   of leakage on the failure scenarios that have an interesting `Internal`
   (`GW-MP-04`, `GW-S3-06`, `GW-URL-12`).
5. **Unknown routes are not authenticated.** `requireToken` is per-route, so
   `GET /nope` with no token returns **404 plain text** (`404 page not found`),
   not 401 and not the JSON envelope. That is intended; assert it as such.

---

## 4. Test profiles

A profile is a set of environment variables the gateway is (re)started with.
`scripts/profile.sh <name>` writes `/etc/printgateway/printgateway.env` and
restarts the systemd unit; `scripts/run-newman.sh <name>` then runs the
collection with the matching Postman environment.

| Profile | Token source | Object store | Fetch guard | Limits | Purpose |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **A — baseline** | `PRINT_GATEWAY_TOKEN` | disabled | strict | defaults | The default posture. Most scenarios run here. |
| **B — vault** | local-vault `config/print_gateway`/`auth-token` | disabled | strict | defaults | Vault-sourced secrets. |
| **C — s3** | `PRINT_GATEWAY_TOKEN` | **`S3_BACKEND=local\|internal\|aws`** | strict | defaults | Object store, run once per backend. |
| **D — vault+s3** | Vault | Vault-sourced creds, `S3_BACKEND=…` | strict | defaults | Both secrets layers at once. |
| **E — permissive** | `PRINT_GATEWAY_TOKEN` | disabled | `ALLOW_PRIVATE_TARGETS=true` | defaults | The **only** profile where a `file_url` happy path is possible against a local fixture. |
| **F — allowlist** | `PRINT_GATEWAY_TOKEN` | as C | strict + `FETCH_ALLOWED_HOSTS` | defaults | Host-suffix allowlist, positive and negative. |
| **G — limits** | `PRINT_GATEWAY_TOKEN` | as C | strict | **tight** | 413 paths without needing 64 MiB fixtures. |
| **X — startup** | varies | varies | varies | varies | One-shot runs that assert the process **refuses to start** (or starts degraded). Never serves traffic. |

**Profile G's tight limits** — chosen so the existing 2.5 MB `printDemo.pdf` and
a few hundred bytes of JSON are enough to trip every size gate:

```
PRINT_GATEWAY_MAX_UPLOAD_BYTES=1048576      # 1 MiB  — printDemo.pdf (2.5 MB) exceeds it
PRINT_GATEWAY_MAX_JSON_BYTES=256            # 256 B  — a padded JSON body exceeds it
PRINT_GATEWAY_FETCH_MAX_BYTES=65536         # 64 KiB
PRINT_GATEWAY_S3_MAX_BYTES=65536            # 64 KiB
```

### 4.1 Why `file_url` needs its own profile

Under the default guard, a successful `file_url` print is **impossible against
anything local**: the guard rejects loopback/private/link-local addresses *and*
every port except 80/443, checked at `net.Dialer.Control` on the resolved IP. So:

- A **local MinIO presigned URL** (`http://127.0.0.1:9010/…`) is blocked twice
  over — profile **E** is required to print it via `file_url`.
- A **real AWS S3 presigned URL** (`https://bucket.s3.region.amazonaws.com/…`) is
  public, HTTPS, port 443 → it passes the strict guard. **This is the only
  configuration in which the `file_url` happy path is exercised with the
  production security posture intact** (`E2E-04`), which makes the AWS backend
  worth running even though the local one is faster to iterate against.

### 4.2 Object-store backends

The same `C`/`D` scenarios run unchanged against all three; only the env differs.

| `S3_BACKEND` | Endpoint | TLS | Region | Credentials | Notes |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `local` | `127.0.0.1:9010` | `S3_INSECURE=true` | `us-east-1` | generated by `setup-minio.sh` | Fastest loop. Stood up as a WSL systemd unit (port 9000 is taken by portainer on this box). |
| `internal` | *your internal MinIO* | per deployment | **must be set** | supplied | **Needs from you:** endpoint, bucket, region, access/secret key. |
| `aws` | `s3.<region>.amazonaws.com` | `S3_INSECURE=false` | **must be set** | IAM keys | **Needs from you:** bucket, region, IAM keys with `s3:GetObject`/`s3:PutObject` on it. Requires egress from WSL. |

`cloud_storage.NewS3` passes the endpoint straight to `minio.New`, which detects
`*.amazonaws.com` and switches to virtual-host addressing and SigV4 — so real
AWS works with **no code change**, provided `PRINT_GATEWAY_S3_REGION` is set.
Leaving the region empty against AWS costs a live bucket-location round trip and,
if that fails, silently signs as `us-east-1`, producing presigned URLs that fail
for whoever uses them with nothing server-side to trace it back to
(`GW-CFG-07` asserts the warning is logged).

---

## 5. Prerequisites

### 5.1 Global (all profiles)

| # | Prerequisite | Verify with | Fix with |
| :--- | :--- | :--- | :--- |
| P1 | WSL distro `Ubuntu` running | `wsl -l -v` | `wsl -d Ubuntu -u root` |
| P2 | `cups.service` active, socket/path activation **disabled** | `systemctl is-active cups; systemctl is-enabled cups.socket cups.path` | `systemctl disable --now cups.socket cups.path; systemctl enable --now cups` |
| P3 | Queue `brother-direct` exists and is enabled | `lpstat -p brother-direct` | see `docs/STATUS.md` fifth phase |
| P4 | Real printer reachable (192.168.252.210) | `printersearch info -host 192.168.252.210` | network/printer power |
| P5 | Gateway binary current | `scripts/deploy.sh` (builds with `-ldflags` so `/status` reports a real version) | rebuild + `scripts/deploy.sh` |
| P6 | `newman` available | `npx newman --version` | `npm i -g newman` |
| P7 | Fixtures present | `ls tests/testdata` | see §5.4 |

> **Paper warning.** Every scenario tagged **`PAPER`** physically prints on the
> Brother MFC-L2700DW. `run-newman.sh` excludes that folder unless
> `--with-paper` is passed. Non-paper print scenarios target `vp1` (a virtual
> `ippeveprinter` queue) instead.

### 5.2 Per profile

| Profile | Extra prerequisite | Verify |
| :--- | :--- | :--- |
| B, D | `local-vault` container up; secret seeded | `docker ps \| grep local-vault`; `scripts/setup-vault.sh --verify` |
| C, D, F, G | Object store reachable, bucket exists, fixtures uploaded | `scripts/setup-minio.sh --verify` (local) / `--backend internal\|aws --verify` |
| E | Fixture HTTP server on `127.0.0.1:8099` | `scripts/fixtures-server.sh status` |
| — (bench) | 15 `q-*` queues + 15 `ippeve-*` units | `lpstat -p \| grep -c '^printer q-'` → 15 |

> **Currently missing on this box** (checked 2026-09-03): the 15 `q-*` queues and
> 14 of the 15 `ippeve-*` printers are gone — only `brother-direct`,
> `brother-fixed` and `vp1` survive. Re-run `src/ops/setup-15-printers.sh` then
> `src/ops/setup-15-queues.sh` before any `IPP-BENCH-*` scenario. Also: there is
> no `/etc/printgateway/` directory and the live unit carries a hardcoded
> `PRINT_GATEWAY_TOKEN=` in `Environment=` while binding `0.0.0.0:8090`;
> `profile.sh` replaces that with an `EnvironmentFile=` + `127.0.0.1:8090` unit
> as its first action.

### 5.3 Object-store seed data

`setup-minio.sh` uploads these into the bucket for every backend:

| Key | Content | Used by |
| :--- | :--- | :--- |
| `test/printDemo.pdf` | the 2.5 MB 2-page fixture | `GW-S3-02`, `E2E-03` |
| `test/small.pdf` | 1-page, <64 KiB | `GW-S3-02` under profile G |
| `test/large.bin` | 128 KiB of zeros | `GW-S3-05` (413 under profile G) |
| `test/not-a-pdf.txt` | `hello` | `GW-S3-07` (documented gap) |
| `test/empty.pdf` | 0 bytes | `GW-S3-08` (documented gap) |
| *(absent)* `test/does-not-exist.pdf` | — | `GW-S3-03` (404) |

### 5.4 Local fixtures (`tests/testdata/`)

| File | Purpose |
| :--- | :--- |
| `printDemo.pdf` | 2.5 MB, 2 pages — the standard document (symlink/copy of the repo root one) |
| `small.pdf` | <64 KiB single page |
| `not-a-pdf.txt` | non-PDF content, asserts the documented no-validation gap |
| `empty.pdf` | 0 bytes, same |
| `large.bin` | 2 MiB, trips profile G's upload limit |
| `weird-name.pdf` | filename `../../etc/pa ssw"rd.pdf` — asserts spool-name sanitizing |

### 5.5 Fixture HTTP server (profile E)

`fixtures-server.sh` serves `tests/testdata/` on `127.0.0.1:8099` plus four
synthetic routes the SSRF/limit scenarios need:

| Route | Behavior | Used by |
| :--- | :--- | :--- |
| `/printDemo.pdf` | 200 + the PDF | `GW-URL-01` |
| `/redirect` | 302 → `/printDemo.pdf` | `GW-URL-07` |
| `/notfound` | 404 | `GW-URL-09` |
| `/lying-chunked` | chunked, no `Content-Length`, streams > max | `GW-URL-11` |
| `/slow` | sends headers, then stalls 120 s | `GW-URL-13` |

---

## 6. Gateway — auth, routing, correlation

Profile **A** unless noted. None of these contact a printer.

| ID | Scenario | Request | Expected |
| :--- | :--- | :--- | :--- |
| GW-AUTH-01 | No token | `POST /print`, no `X-Labos-Print-Token` | 401, details `unauthorized`, `X-Laas-Identifier` present |
| GW-AUTH-02 | Wrong token | `POST /print`, token `nope` | 401, `unauthorized` |
| GW-AUTH-03 | Correct token | valid token, otherwise empty body | **not** 401 (415 — proves auth passed) |
| GW-AUTH-04 | Token with correct prefix, wrong suffix | `<token>x` | 401 (constant-time compare, no partial match) |
| GW-AUTH-05 | Token on `/files/presign` | no token | 401 — both routes are guarded |
| GW-AUTH-06 | `GET /print` | valid token | 405, `use POST` |
| GW-AUTH-07 | `PUT /files/presign` | valid token | 405, `use POST` |
| GW-AUTH-08 | Unknown route | `GET /nope`, **no token** | 404, plain text `404 page not found`, **not** the JSON envelope (see §3.5) |
| GW-AUTH-09 | Trailing slash | `POST /print/` | 404 — `/print` is an exact ServeMux pattern |
| GW-AUTH-10 | Unsupported Content-Type | `text/plain` | 415, `Content-Type must be multipart/form-data … or application/json …` |
| GW-AUTH-11 | Missing Content-Type | none | 415 |
| GW-AUTH-12 | Content-Type case/params | `MULTIPART/FORM-DATA; boundary=x` | routed as multipart (400 for the bad body, **not** 415) — `mime.ParseMediaType`, not a prefix match |
| GW-ID-01 | Correlation id echoed | send `X-Laas-Identifier: e2e-abc-123` | response header **equals** it |
| GW-ID-02 | Correlation id generated | send none | response header present, matches `^req-` |
| GW-ID-03 | Non-printable id rejected | value containing `\x01` | response header ≠ sent value, matches `^req-` |
| GW-ID-04 | Over-length id rejected | 129 printable chars | response header ≠ sent value, matches `^req-` |
| GW-ID-05 | Id on a 401 | bad token + supplied id | 401 **and** the id is echoed |
| GW-ID-06 | 128-char id accepted | exactly 128 printable chars | echoed verbatim (boundary) |

## 7. Gateway — multipart intake (attach the file)

| ID | Scenario | Profile | Body | Expected |
| :--- | :--- | :--- | :--- | :--- |
| GW-MP-01 | Happy path, virtual queue | A | `printer=vp1`, `file=printDemo.pdf` | 200, `status=submitted`, `output` matches `request id is vp1-\d+ \(0 file\(s\)\)` — **0**, not 1: `lp` is fed the document on stdin with no path operand, and it counts operands |
| GW-MP-02 **PAPER** | Happy path, real printer | A | `printer=brother-direct`, `file=printDemo.pdf` | 200 + **2 physical pages**; `output` matches `request id is brother-direct-\d+` |
| GW-MP-03 | Missing `printer` | A | file only | 400, `missing url parameter: printer` |
| GW-MP-04 | Missing `file` part | A | printer only | 400, `missing file part:` |
| GW-MP-05 | Empty `printer` value | A | `printer=""` + file | 400, `missing url parameter: printer` |
| GW-MP-06 | Nonexistent queue | A | `printer=no-such-queue` | 500, details **exactly** `print submission failed`; assert body contains **no** `/tmp`, no `lp:`, no `lpstat` |
| GW-MP-07 | Malformed multipart | A | `Content-Type: multipart/form-data` with no boundary | 400, `invalid multipart body:` |
| GW-MP-08 | Oversize upload | **G** | `printDemo.pdf` (2.5 MB) vs 1 MiB limit | 413, `request body exceeds the 1048576 byte limit` |
| GW-MP-09 | Upload at the limit | **G** | `small.pdf` (<1 MiB) | 200 (boundary — the limit is not off-by-one against a legitimate file) |
| GW-MP-10 | Non-PDF content | A | `not-a-pdf.txt` | **200** — documented gap, no content validation (§10) |
| GW-MP-11 | Empty file | A | `empty.pdf` (0 bytes) | **500**, details exactly `print submission failed` — verified live: the gateway validates nothing and spools it, then `lp` refuses it ("No file in print request"). Not the 200 the README implied; both were corrected. |
| GW-MP-12 | Hostile filename | A | `weird-name.pdf` | 200; spool name sanitized (verify in the gateway log, not the response) |
| GW-MP-13 | Extra unknown form field | A | `printer`, `file`, `bogus=1` | 200 — multipart intake is not strict (unlike JSON) |
| GW-MP-14 | Two `file` parts | A | two file parts | 200, first part used (`r.FormFile` semantics) |

## 8. Gateway — JSON contract

Applies to both `POST /print` (JSON form) and `POST /files/presign`; both use the
same `decodeStrictJSON`.

| ID | Scenario | Profile | Body | Expected |
| :--- | :--- | :--- | :--- | :--- |
| GW-JSON-01 | Malformed JSON | A | `{ not valid` | 400, `invalid JSON body:` |
| GW-JSON-02 | Unknown field | A | `{"printer":"vp1","fiel_url":"…"}` | 400, `invalid JSON body:` + `unknown field` |
| GW-JSON-03 | Two concatenated values | A | `{"printer":"vp1","s3_key":"a"}{"x":1}` | 400, `body must contain exactly one JSON value` |
| GW-JSON-04 | Trailing garbage | A | `{"printer":"vp1","s3_key":"a"} oops` | 400 |
| GW-JSON-05 | Missing `printer` | A | `{"file_url":"https://example.com/a.pdf"}` | 400, `printer is required` |
| GW-JSON-06 | Neither reference | A | `{"printer":"vp1"}` | 400, `exactly one of file_url or s3_key is required` |
| GW-JSON-07 | Both references | A | `{"printer":"vp1","file_url":"…","s3_key":"…"}` | 400, `exactly one of file_url or s3_key is required` |
| GW-JSON-08 | Case-insensitive field names | A | `{"Printer":"vp1","S3_Key":"a"}` | **accepted** (503/200 per profile, **not** 400) — documented `encoding/json` behavior |
| GW-JSON-09 | Duplicate key, last wins | A | `{"printer":"bad","printer":"vp1","s3_key":"a"}` | resolves to `vp1` — documented behavior, not rejected |
| GW-JSON-10 | Oversize JSON body | **G** | 512 B of padding vs 256 B limit | 413, `request body exceeds the 256 byte limit` |
| GW-JSON-11 | Empty body | A | `` with `application/json` | 400, `invalid JSON body: EOF` |
| GW-JSON-12 | JSON null body | A | `null` | 400, `printer is required` |
| GW-JSON-13 | `charset` parameter | A | `application/json; charset=utf-8` | routed as JSON (prefix match) — 400 on missing fields, not 415 |

## 9. Gateway — `file_url` intake and SSRF guard

Profile **A** (strict) unless noted. **Every row in this table except GW-URL-01
and the two allowlist-positive rows is a security assertion** — a 200 where a
4xx is expected is a live SSRF hole, not a test bug.

| ID | Scenario | Profile | `file_url` | Expected |
| :--- | :--- | :--- | :--- | :--- |
| GW-URL-01 | Happy path | **E** | `http://127.0.0.1:8099/printDemo.pdf` | 200, printed to `vp1` |
| GW-URL-02 | Loopback blocked | A | `http://127.0.0.1/x.pdf` | 400, `file_url resolved to a disallowed address` |
| GW-URL-03 | CUPS admin port | A | `http://127.0.0.1:631/admin` | 400, `file_url port 631 is not allowed (only 80/443)` — the port gate fires before the address gate |
| GW-URL-04 | Cloud metadata | A | `http://169.254.169.254/latest/meta-data/` | 400, `file_url resolved to a disallowed address` |
| GW-URL-05 | RFC1918 | A | `http://192.168.252.210/` | 400, disallowed address |
| GW-URL-06 | Bad scheme | A | `ftp://example.com/a.pdf` | 400, `file_url scheme must be http or https, got "ftp"` |
| GW-URL-07 | `file://` | A | `file:///etc/passwd` | 400, scheme message |
| GW-URL-08 | Embedded credentials | A | `http://u:p@example.com/a.pdf` | 400, `file_url must not contain embedded credentials` |
| GW-URL-09 | No host | A | `http:///a.pdf` | 400, `file_url must name a host` |
| GW-URL-10 | Not a URL | A | `not a url` | 400, `invalid file_url:` |
| GW-URL-11 | Redirect not followed | **E** | `http://127.0.0.1:8099/redirect` | 400, `file_url must be a direct link: redirects are not followed` |
| GW-URL-12 | Remote 404 | **E** | `http://127.0.0.1:8099/notfound` | 502, `file_url returned an error`; body leaks no upstream content |
| GW-URL-13 | Unreachable port | **E** | `http://127.0.0.1:1/x.pdf` | 502, `failed to download file_url` |
| GW-URL-14 | Oversize, honest `Content-Length` | **E+G** | `http://127.0.0.1:8099/printDemo.pdf` vs 64 KiB | 413, `file_url response is too large` |
| GW-URL-15 | Oversize, chunked/lying | **E+G** | `http://127.0.0.1:8099/lying-chunked` | 413, same — proves the `LimitReader` catches what `Content-Length` didn't |
| GW-URL-16 | Fetch timeout | **E**, `FETCH_TIMEOUT=2s` | `http://127.0.0.1:8099/slow` | 502 within ~3 s (not the 8 m write timeout) |
| GW-URL-17 | Allowlist — blocked host | **F** (`FETCH_ALLOWED_HOSTS=s3.amazonaws.com`) | `https://example.com/a.pdf` | **403**, `file_url host is not allowed` (403, not 400 — a distinct gate) |
| GW-URL-18 | Allowlist — allowed host, still address-checked | **F** + `ALLOW_PRIVATE=false` | allowed suffix resolving to a private IP | 400, disallowed address — the allowlist does **not** lift the address gate |
| GW-URL-19 | Permissive lifts port too | **E** | `http://127.0.0.1:8099/…` (port 8099) | 200 — confirms `ALLOW_PRIVATE_TARGETS` lifts the port rule, not only the address rule |
| GW-URL-20 | Permissive does **not** lift the allowlist | **E** + `FETCH_ALLOWED_HOSTS=example.com` | `http://127.0.0.1:8099/…` | 403 — the two knobs are independent |
| GW-URL-21 | IPv6-mapped IPv4 evasion | A | `http://[::ffff:127.0.0.1]/x.pdf` | 400, disallowed address |
| GW-URL-22 | DNS rebinding | A | a hostname resolving to `127.0.0.1` | 400, disallowed address — the gate runs on the resolved IP at `connect(2)` |

> `GW-URL-22` needs a public hostname with an A record pointing at 127.0.0.1.
> Use a resolver-service name if one is reachable, otherwise mark **SKIPPED —
> environment** rather than deleting the row; the property it asserts is the
> single most important one in the guard.

## 10. Gateway — `s3_key` intake

Profile **C** (or **D**) unless noted; run the whole table once per
`S3_BACKEND` value.

| ID | Scenario | Profile | Body | Expected |
| :--- | :--- | :--- | :--- | :--- |
| GW-S3-01 | Object storage disabled | **A** | `{"printer":"vp1","s3_key":"test/printDemo.pdf"}` | 503, `object storage is not configured` |
| GW-S3-02 | Happy path | C | `s3_key=test/printDemo.pdf` | 200, `request id is vp1-\d+` |
| GW-S3-03 **PAPER** | Happy path, real printer | C | `printer=brother-direct` | 200 + 2 physical pages |
| GW-S3-04 | Missing key | C | `s3_key=test/does-not-exist.pdf` | 404, `object "test/does-not-exist.pdf" not found` |
| GW-S3-05 | Path traversal | C | `s3_key=../other-bucket/x.pdf` | 400, `s3_key must not contain path traversal segments` |
| GW-S3-06 | Traversal, encoded | C | `s3_key=a/./../../x.pdf` | 400, same (`path.Clean` form check) |
| GW-S3-07 | Oversize object | **C+G** | `s3_key=test/large.bin` (128 KiB vs 64 KiB) | 413, `object exceeds the maximum allowed size of 65536 bytes` |
| GW-S3-08 | Store unreachable | C, endpoint pointed at a dead port | any key | 502, `failed to fetch object from storage`; **no** endpoint/credential text in the body |
| GW-S3-09 | Bad credentials | C, wrong secret key | any key | 502, `failed to fetch object from storage` (not 404 — auth failure is not a miss) |
| GW-S3-10 | Non-PDF object | C | `s3_key=test/not-a-pdf.txt` | **200** — documented gap (same as `GW-MP-10`) |
| GW-S3-11 | Empty object | C | `s3_key=test/empty.pdf` | **500**, `print submission failed` — the `s3_key` twin of `GW-MP-11`; see that row |
| GW-S3-12 | Key with spaces/unicode | C | `s3_key=test/a b ü.pdf` (seeded) | 200 — key is used verbatim, only traversal is rejected |
| GW-S3-13 | S3 timeout | C, `S3_TIMEOUT=1s`, throttled endpoint | large object | 504 `print submission timed out` or 502 — assert it fails fast, not at the write timeout |

## 11. Gateway — `POST /files/presign`

| ID | Scenario | Profile | Body | Expected |
| :--- | :--- | :--- | :--- | :--- |
| GW-PRE-01 | Storage disabled | **A** | `{"key":"test/a.pdf"}` | 503, `object storage is not configured` |
| GW-PRE-02 | Default GET | C | `{"key":"test/printDemo.pdf"}` | 200; `url` non-empty, `key` echoed, `expires_at` ≈ now + 15 m (±60 s) |
| GW-PRE-03 | Explicit GET | C | `+"method":"GET"` | 200, same |
| GW-PRE-04 | PUT | C | `"method":"PUT"` | 200; URL usable for upload (see `E2E-03`) |
| GW-PRE-05 | Lowercase method | C | `"method":"put"` | 200 — uppercased before validation |
| GW-PRE-06 | Invalid method | C | `"method":"DELETE"` | 400, `method must be GET or PUT` |
| GW-PRE-07 | Shorter TTL honored | C | `"ttl_seconds":60` | 200, `expires_at` ≈ now + 60 s |
| GW-PRE-08 | Longer TTL clamped | C | `"ttl_seconds":86400` | 200, `expires_at` ≈ now + 15 m (clamped, **not** 400) |
| GW-PRE-09 | Absurd TTL clamped, not negative | C | `"ttl_seconds":10000000000` | 200, `expires_at` **after** now (guards the int64 overflow path) |
| GW-PRE-10 | Zero TTL → default | C | `"ttl_seconds":0` | 200, ≈ now + 15 m |
| GW-PRE-11 | Negative TTL → default | C | `"ttl_seconds":-5` | 200, ≈ now + 15 m |
| GW-PRE-12 | Missing key | C | `{}` | 400, `key is required` |
| GW-PRE-13 | Traversal key | C | `{"key":"../x"}` | 400, `key must not contain path traversal segments` |
| GW-PRE-14 | Nonexistent key | C | `{"key":"test/nope.pdf"}` | **200** — presign does not verify existence; the URL then 404s at the store |
| GW-PRE-15 | Unknown JSON field | C | `{"key":"a","ttl":5}` | 400 (strict decode) |
| GW-PRE-16 | No token | C | valid body | 401 |
| GW-PRE-17 | URL never logged | C | any | gateway log line reads `presigned GET issued: key="…" ttl=…` and contains **no** signature — assert by grepping the log, not the response |

## 12. Gateway — secrets and startup (profiles B, D, X)

These assert **process behavior**, so they are driven by `scripts/profile.sh`
and `scripts/startup-matrix.sh`, not by Postman — except the "does this token
authenticate" half, which is one Postman request per case.

| ID | Scenario | Setup | Expected |
| :--- | :--- | :--- | :--- |
| GW-SEC-01 | Token from Vault | B: `VAULT_ADDR` set, secret seeded, **no** `PRINT_GATEWAY_TOKEN` | starts; log says source `vault`; the Vault token authenticates (200/415), the old env token 401s |
| GW-SEC-02 | Vault wins over env | B + a *different* `PRINT_GATEWAY_TOKEN` | Vault token authenticates, env token 401s; log source `vault` |
| GW-SEC-03 | `SECRET_STORE_URL` overrides `VAULT_ADDR` | both set, only the former valid | starts against `SECRET_STORE_URL` |
| GW-SEC-04 | Vault down, env present | `VAULT_ADDR` → dead port, env token set | starts; log `env (vault fallback)` + one error line; env token authenticates |
| GW-SEC-05 | Vault key blank | seed `auth-token=""` , env token set | starts; log names the empty value and falls back; env token authenticates |
| GW-SEC-06 | Vault down, **no** env token | dead Vault, no env token | **exit 1**, no listener on 8090, stderr names the failure |
| GW-SEC-07 | No Vault, no env token | neither set | **exit 1**, message `print token unavailable: vault is not configured and PRINT_GATEWAY_TOKEN is not set` |
| GW-SEC-08 | Whitespace-only env token | `PRINT_GATEWAY_TOKEN="   "` | **exit 1** — trimmed, treated as unset |
| GW-SEC-09 | Plaintext `SECRET_STORE_PASSWORD` | userpass auth, password not `encryption.Encrypt`-ed | Vault init fails → falls back to env; **not** a crash |
| GW-SEC-10 | `LABOS_ENV` prefix | `LABOS_ENV=dev`, secret seeded at `dev/config/print_gateway` | starts, source `vault`; **without** the prefixed secret it falls back |
| GW-SEC-11 | S3 credentials from Vault | D: `s3-access-key`/`s3-secret-key` seeded, **no** env S3 creds | `GW-S3-02` passes |
| GW-SEC-12 | S3 creds — Vault miss, env fallback | D minus the Vault keys, env creds set | `GW-S3-02` still passes |
| GW-SEC-13 | Log server from Vault | `log-server` seeded → `logstash:514` | log says source `vault`; records queryable in Kibana by `job_id` (`kibana-search` skill) |
| GW-SEC-14 | Log server from env | `LOG_SERVER=…:514`, no Vault key | log says source `env` |
| GW-SEC-15 | Log server unresolvable | `LOG_SERVER=nope:99999` | starts console-only; one error line; **not** a crash |
| GW-SEC-16 | Token value never logged | any of the above | `journalctl -u printgateway` contains the token **zero** times |

## 13. Gateway — configuration validation (profile X)

Each row is a one-shot `printgateway` launch that must **fail fast and name the
offending variable**, or start degraded. None serve traffic.

| ID | Variable(s) | Value | Expected |
| :--- | :--- | :--- | :--- |
| GW-CFG-01 | `PRINT_GATEWAY_READ_TIMEOUT` | `0s` | exit 1, message names the variable |
| GW-CFG-02 | `PRINT_GATEWAY_READ_TIMEOUT` | `-5s` | exit 1 |
| GW-CFG-03 | `PRINT_GATEWAY_IDLE_TIMEOUT` | `banana` | exit 1 (unparsable) |
| GW-CFG-04 | `PRINT_GATEWAY_READ_HEADER_TIMEOUT` | > read timeout | exit 1 (inconsistent) |
| GW-CFG-05 | `PRINT_GATEWAY_WRITE_TIMEOUT` | ≤ read + max(fetch,S3) + submit | exit 1 — the double-print guard (README "Why the write timeout is the largest value") |
| GW-CFG-06 | `PRINT_GATEWAY_SHUTDOWN_GRACE` | ≤ max(fetch,S3) + submit | exit 1 |
| GW-CFG-07 | `PRINT_GATEWAY_MAX_HEADER_BYTES` | `64KiB` | exit 1 — plain byte count only |
| GW-CFG-08 | `PRINT_GATEWAY_S3_ENDPOINT` set, `..._BUCKET` empty | — | **starts**, logs loudly, S3 disabled → `GW-S3-01` behavior. Never a startup failure. |
| GW-CFG-09 | `..._BUCKET` set, `..._ENDPOINT` empty | — | same as 08 |
| GW-CFG-10 | S3 configured, `..._REGION` empty | — | starts; `LogError` warning present in the log |
| GW-CFG-11 | `PRINT_GATEWAY_LOG_LEVEL` | `verbose` | starts at `info`, logs the invalid value — logging misconfig is not fatal |
| GW-CFG-12 | `PRINT_GATEWAY_LOG_LEVEL` | `debug` | starts; debug lines present |
| GW-CFG-13 | `PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS` | `true` | starts; **`LogError`-level** warning present regardless of log level |
| GW-CFG-14 | Listen address arg | `printgateway 127.0.0.1:8090` | binds loopback only — assert unreachable from Windows |
| GW-CFG-15 | Listen address arg | `printgateway :8090` | binds all interfaces — assert reachable from Windows (documents the risk; do not leave running) |

## 14. Gateway — operational behavior

| ID | Scenario | Method | Expected |
| :--- | :--- | :--- | :--- |
| GW-OPS-01 | Graceful shutdown | start a slow print, `systemctl stop printgateway` mid-flight | the in-flight request completes with 200; process exits within the grace period |
| GW-OPS-02 | Shutdown timeout bound | same, with a wedged queue | process exits at `SHUTDOWN_GRACE`, does not hang forever |
| GW-OPS-03 | `lp` timeout | `SUBMIT_TIMEOUT=1s` against a paused queue (`cupsdisable vp1`) | 504, `print submission timed out` — not a hang |
| GW-OPS-04 | Read-header timeout | open a socket, send a partial request line, wait 15 s | connection closed; a `net/http` error line appears in the **gateway's own** logger (not raw stderr) |
| GW-OPS-05 | Concurrency smoke | 20 parallel `GW-MP-01` | all 200, 20 distinct CUPS job ids, no interleaved/corrupted responses. **Documents the known no-limit gap** (§15) rather than asserting a limit |
| GW-OPS-06 | Completion log per request | any request | exactly one `LogAPICompletion` line with `job_id`/`duration`/`status` matching the response |
| GW-OPS-07 | Completion log on 401 | `GW-AUTH-01` | a completion line exists with `status=401` |
| GW-OPS-08 | Spool cleanup | run `GW-MP-01` 10× | no `print-upload-*`/`print-download-*`/`print-s3-*` files left in the temp dir |
| GW-OPS-09 | Restart resilience | `systemctl restart printgateway` | comes back within 5 s, `GW-MP-01` passes again |
| GW-OPS-10 | Kibana traceability | `GW-MP-01` with a known `X-Laas-Identifier`, profile with `LOG_SERVER` | that `job_id` is findable in Kibana (`kibana-search` skill) |

## 15. printersearch — IPP client (`scripts/ipp-suite.sh`)

Targets: `REAL` = 192.168.252.210:631/ipp/print · `QUEUE` =
127.0.0.1:631/printers/brother-direct · `VIRT` = 127.0.0.1:9101/ipp/print ·
`FIX` = 127.0.0.1:6310/ipp/print.

| ID | Scenario | Target | Expected |
| :--- | :--- | :--- | :--- |
| IPP-01 | `info` | REAL | exit 0; `successful-ok`; `printer-state` present; any `[REJECTED BY PRINTER]` markers reported |
| IPP-02 | `info` | QUEUE | exit 0; CUPS-shaped attributes |
| IPP-03 | `info` | VIRT | exit 0 |
| IPP-04 | `info` — missing `-host` | — | exit 1, usage on **stderr** |
| IPP-05 | `info` — unreachable host | `10.255.255.1` with `-timeout 2s` | exit 1 within ~3 s (proves `-timeout` is wired) |
| IPP-06 | `info` — wrong path | QUEUE + `/nope` | exit 1, IPP status name reported (not a panic) |
| IPP-07 **PAPER** | `print` default | QUEUE | exit 0, job id printed, 2 physical pages |
| IPP-08 | `print` to virtual | VIRT, `-document-format application/octet-stream` | exit 0 |
| IPP-09 **PAPER** | `print -resolution 600` | QUEUE | exit 0; assert the attribute is **not** in the rejected group (the `printer-resolution` vs `print-resolution` trap) |
| IPP-10 **PAPER** | `print -copies 2 -media iso_a4_210x297mm -color-mode monochrome` | QUEUE | exit 0, 4 pages |
| IPP-11 **PAPER** | `print -first-page 1 -last-page 1` | QUEUE | 1 page |
| IPP-12 | `print -hold` → `jobs` → `cancel` | QUEUE | held job appears in `jobs`, `cancel` returns `successful-ok`, job gone — **no paper** |
| IPP-13 | `print` — missing `-file` | QUEUE | exit 1 |
| IPP-14 | `print` — nonexistent file | QUEUE | exit 1, `error opening file` |
| IPP-15 | `print` — non-regular file (FIFO) | QUEUE | exit 1, `is not a regular file` — the silent-truncation guard |
| IPP-16 | `print` direct to printer | REAL | exit 0 — proves the CUPS-vs-direct interchangeability the benchmarks rest on |
| IPP-17 | `jobs` on an empty queue | QUEUE | exit 0, no jobs listed |
| IPP-18 | `cancel` — nonexistent job id | QUEUE | exit 1 or a reported IPP error status; no panic |
| IPP-19 | `cancel` — missing `-job-id` | QUEUE | exit 1 |
| IPP-20 | Oversized attribute clamp | `-job-name` of 80 000 chars | exit 0 or a clean error — never a wire-framing corruption/panic (the `uint16` clamp) |
| IPP-BENCH-01 | Baseline bench | `-paths` across 3 `q-*` queues, `-requests 30 -concurrency 3` | exit 0; `fail=0`; percentiles reported |
| IPP-BENCH-02 | JSON output | `+ -json` | parses as JSON; same fields |
| IPP-BENCH-03 | `-wait-completion` | `+ -wait-completion -poll-timeout 30s` | separate `completed` latency reported |
| IPP-BENCH-04 | Failure accounting | one bogus path in `-paths` | `fail>0`; **percentiles computed over successes only** (the sixth-phase fix) |
| IPP-BENCH-05 | Load run | 15 queues, `-requests 700 -concurrency 20` | `fail=0` — requires `MaxJobs 2000` (§5.2); a "Too many active jobs" failure means the cupsd.conf change was lost |

## 16. ippfix

| ID | Scenario | Expected |
| :--- | :--- | :--- |
| FIX-01 | `-gen-template` against the real printer | writes a `printer-template.json` with the printer-attributes group |
| FIX-02 | Unfiltered `Get-Printer-Attributes` through FIX | `successful-ok`; empty `naturalLanguage` values replaced with `en-us`; template gaps filled |
| FIX-03 | **Filtered** `requested-attributes` through FIX | **`server-error-internal-error`** — the documented, unfixed bug (`docs/STATUS.md` fifth phase). Asserted as an *expected failure*: if this ever returns `successful-ok`, the bug was fixed and this row plus the STATUS entry must be updated |
| FIX-04 | `driverless cat ipp://127.0.0.1:6310/ipp/print` | emits a usable PPD |
| FIX-05 | Print through a queue pointed at FIX | **expected to stall in "printing"** — the same bug; this is why `brother-direct` bypasses `ippfix` |

## 17. Cross-component end-to-end chains

| ID | Chain | Profile | Expected |
| :--- | :--- | :--- | :--- |
| E2E-01 | `presign PUT` → upload the PDF to that URL → `POST /print` with `s3_key` | C | 200 at every step; document prints. **The recommended large-file path** — no bytes cross the gateway on intake |
| E2E-02 | `presign GET` → fetch the URL directly with curl | C | bytes match the seeded object |
| E2E-03 **PAPER** | E2E-01 against `brother-direct` | C | 2 physical pages |
| E2E-04 | `presign GET` (**AWS**) → `POST /print` with that URL as `file_url` | **C/`aws`, strict guard** | 200 — the only `file_url` happy path that runs with the production guard intact (§4.1) |
| E2E-05 | Same as E2E-04 against **local MinIO** | C/`local`, strict guard | **400 disallowed address** — proves the guard is doing its job, and documents why profile E exists |
| E2E-06 | Expired presigned URL | C, `ttl_seconds=5`, wait 10 s | the store rejects it (403); gateway reports 502 `file_url returned an error` |
| E2E-07 | Gateway print → `printersearch jobs` on the same queue | A | the job submitted over HTTP is visible over raw IPP — the two paths address the same queue |
| E2E-08 | Correlation id end-to-end | A + `LOG_SERVER` | one caller-supplied id appears in the response header, the gateway log, and Kibana |

## 18. Documented gaps asserted as current behavior

These rows assert what the service **does today**, matching the README's
"deliberately not here yet". They are not bugs and must not be "fixed" in the
test suite by changing the expectation — if behavior changes, the README changes
with it.

| Behavior | Scenarios | README section |
| :--- | :--- | :--- |
| No content-type / magic-byte validation | `GW-MP-10`, `GW-S3-10` (both 200) | "No content validation of a fetched or uploaded document" |
| No minimum-size validation — but `lp` refuses a 0-byte job, so it surfaces as 500 rather than 200 | `GW-MP-11`, `GW-S3-11` | same section (corrected 2026-09-07 after the suite disproved the "empty … spooled as a success" wording) |
| No concurrency limit on `/print` | `GW-OPS-05` | "No concurrency limit on `/print`" |
| No per-caller bucket restriction | `GW-PRE-14`, `GW-S3-12` | "No per-caller bucket restriction" |
| No status endpoint, no queue, no retry, no audit trail | *(none — nothing to call)* | "What's deliberately not here yet" |
| No `/health` or `/ready` | `GW-AUTH-08` (404) | `server.go` comment |
| Case-insensitive JSON fields, duplicate-key last-wins | `GW-JSON-08`, `GW-JSON-09` | "decoded strictly" paragraph |
| `ippfix` breaks on filtered attribute requests | `FIX-03`, `FIX-05` | `docs/STATUS.md` fifth phase |
| Egress proxies are not honored for `file_url` | *(document only)* | "SSRF defense" |

## 19. Runbooks — what to run, in what order

Each runbook is self-contained: prerequisites, command, expected duration.

### R0 — Smoke (≈2 min, run first, always)
```bash
scripts/profile.sh A
scripts/run-newman.sh A --folder "Auth, routing & correlation"
scripts/run-newman.sh A --folder "Multipart intake" --grep GW-MP-01
```
Green ⇒ the gateway is up, authenticating, and printing to a virtual queue.
Red ⇒ stop; nothing below will be meaningful.

### R1 — Baseline contract (≈5 min, no printer, no paper)
```bash
scripts/profile.sh A && scripts/run-newman.sh A
```
Covers §6, §7 (minus PAPER), §8, the strict-guard rows of §9, `GW-S3-01`,
`GW-PRE-01`.

### R2 — Fetch / SSRF (≈4 min)
```bash
scripts/fixtures-server.sh start
scripts/profile.sh E && scripts/run-newman.sh E --folder "file_url intake"
scripts/profile.sh F && scripts/run-newman.sh F --folder "file_url intake"
```

### R3 — Limits (≈3 min)
```bash
scripts/profile.sh G && scripts/run-newman.sh G --folder "Limits"
```

### R4 — Object store (≈6 min **per backend**)
```bash
scripts/setup-minio.sh --backend local            # once
for b in local internal aws; do
  scripts/profile.sh C --s3-backend $b
  scripts/run-newman.sh C --folder "s3_key intake" --folder "Presign"
done
```
`internal` and `aws` need credentials in `tests/.env.local` (git-ignored) —
see §4.2.

### R5 — Vault (≈5 min)
```bash
scripts/setup-vault.sh --seed
scripts/profile.sh B && scripts/run-newman.sh B --folder "Auth, routing & correlation"
scripts/profile.sh D --s3-backend local && scripts/run-newman.sh D --folder "s3_key intake"
```

### R6 — Startup matrix (≈4 min, no server left running)
```bash
scripts/startup-matrix.sh          # §12 GW-SEC-06..08 and all of §13
```

### R7 — IPP / CLI (≈6 min, `--with-paper` for the PAPER rows)
```bash
scripts/ipp-suite.sh --no-paper    # IPP-01..06, 08, 12..20, FIX-01..04
scripts/ipp-suite.sh --with-paper  # adds IPP-07, 09, 10, 11, 16
```

### R8 — Bench / load (≈15 min)
```bash
# prerequisite, currently NOT satisfied on this box:
wsl -d Ubuntu -u root -- bash src/ops/setup-15-printers.sh
wsl -d Ubuntu -u root -- bash src/ops/setup-15-queues.sh
scripts/ipp-suite.sh --bench       # IPP-BENCH-01..05
```

### R9 — Paper confirmation (≈5 min, needs a human at the printer)
```bash
scripts/profile.sh C --s3-backend local
scripts/run-newman.sh C --with-paper --folder "Real paper"
```
Then physically verify: `GW-MP-02` (2 pages), `GW-S3-03` (2 pages),
`E2E-03` (2 pages), `IPP-10` (4 pages), `IPP-11` (1 page).

### R10 — Operational (≈8 min, manual)
§14 — shutdown, timeouts, spool cleanup, Kibana. Driven by
`scripts/ops-checks.sh` with human confirmation of the Kibana rows.

### Full regression
`R0 → R1 → R2 → R3 → R4(local) → R5 → R6 → R7 → R9 → R10`, then R4 against
`internal` and `aws`, then R8. Budget ≈90 min including paper checks.

## 20. Maintenance

- **One assertion style.** Every Postman request asserts (a) status, (b) for
  failures, an `errorDetails.details` substring, (c) for successes, the specific
  success shape. Nothing asserts `errorCode`/`errorMessage` (§3.1).
- **Profile-awareness lives in collection variables**, not in duplicated
  requests: `s3Enabled`, `vaultEnabled`, `allowPrivate`, `tightLimits`. A
  request whose expectation flips between profiles (e.g. `GW-S3-01` vs
  `GW-S3-02`) branches on the variable inside its test script, so there is one
  request per scenario, not one per profile.
- **Scenario IDs are the contract.** Every Postman request name and every
  `ipp-suite.sh` case starts with its ID from this document. A scenario added to
  the automation without a row here, or vice versa, is a defect in the suite.
- **Replacing the old collection.** `src/printgateway/printgateway.postman_collection.json`
  is superseded — it predates `s3_key`, `/files/presign`, Vault, and the SSRF
  fix, and it still documents SSRF as an open gap. It is deleted, not merged.
- **When behavior changes**, update in this order: README → this plan's row →
  the automation. A test suite that disagrees with the README is a documentation
  bug first.
