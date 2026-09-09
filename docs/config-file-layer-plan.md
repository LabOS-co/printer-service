# Plan — `printservice.config.json` as the config source of truth

Status: **implemented**, and reshaped a second time on 2026-09-08 — see §7, which is now the
authoritative description of the JSON document's actual shape. §2's field table and §3's
background reasoning (why provenance/absent-vs-zero matter at all) still hold conceptually, but
§2's JSON example and dotted-path key names are **stale** — they show the pre-§7 flat shape, not
the current `resource/*` one. **§4 and §6 below are also superseded** — they described a
default-path/`-config`-flag design that was deliberately NOT what got built; see the correction
notes inline in each section. The current, accurate reference for operators is always
`src/printgateway/README.md`'s "Configuration file" section — read that first, and treat every
JSON snippet in §2-§6 below as history, not a shape to copy.

Decisions taken with the user on 2026-09-07:

- The file's role is a **runtime config file**, not an API contract.
- Precedence is **JSON file → env var → compiled default** (the file is the authoritative layer).
- **Secrets never appear in the file.** They stay Vault → env, exactly as `internal/secrets` resolves them today.
- Renamed `printservice-api.schema.json` → **`printservice.config.json`**.
- The plan covers **only values this project actually consumes** — see §2.

`printservice` here means the `src/printgateway` module; there is no separate service by that name.

---

## 1. Starting point and the rename

The file as delivered was an OpenAPI 3.0.3 document for `OperationsService` (LAB-19590) — file browsing
under `Bin\Resources`, plus system-container and driver inventory/deploy. It held no configuration values
of any kind: every node was a path, a parameter, an HTTP status, or a response schema. So there was nothing
in it to keep, and nothing from that service's domain is carried into the config format below. **Its entire
body is discarded and rewritten** in stage 1.

**Done already:** `printservice-api.schema.json` → `printservice.config.json` (`git mv` not needed — the
file was untracked). Note that the renamed file still contains the old OpenAPI body until stage 1 runs;
nothing reads it, but it is misleading to anyone who opens it in the meantime.

**Deferred to §6 stage 6:** a real JSON Schema for the config, at `printservice.config.schema.json`, so
editors and CI can validate the file. This is what makes the `.schema.json` name meaningful again rather
than vestigial — it will describe the config, not an API.

---

## 2. The config document

Every key below was chosen by auditing `src/printgateway/internal/config/config.go` against its actual
non-test consumers in `cmd/` and `internal/` — no key is speculative, and no value from any other service
appears. The "consumed by" column is the evidence; a value with no live consumer would not be in the file.

| Key | `Config` field | Consumed by |
|---|---|---|
| `service.addr` | `Addr` | `httpapi.NewServer`, `run`'s listen line |
| `service.logLevel` | `LogLevel` | `logger.SetLogLevel` in `run` |
| `timeouts.readHeader` | `ReadHeaderTimeout` | `httpapi.NewServer` |
| `timeouts.read` | `ReadTimeout` | `httpapi.NewServer` |
| `timeouts.write` | `WriteTimeout` | `httpapi.NewServer` |
| `timeouts.idle` | `IdleTimeout` | `httpapi.NewServer` |
| `timeouts.shutdownGrace` | `ShutdownGrace` | `run`'s `server.Shutdown` context |
| `timeouts.submit` | `SubmitTimeout` | `printgw.Timeouts.Submit` → `cups.LPSubmitter` |
| `limits.maxHeaderBytes` | `MaxHeaderBytes` | `httpapi.NewServer` |
| `limits.maxUploadBytes` | `MaxUploadBytes` | `maxBytes` middleware |
| `limits.maxJsonBytes` | `MaxJSONBytes` | `maxBytes` middleware |
| `fetch.timeout` | `FetchTimeout` | `printgw.Timeouts.Fetch` |
| `fetch.maxBytes` | `FetchMaxBytes` | `fetch.NewSafeFetcher` |
| `fetch.allowedHosts` | `FetchAllowedHosts` | `fetch.NewSafeFetcher`, `run`'s startup log |
| `objectStore.endpoint` | `S3Endpoint` | `newObjectStore` |
| `objectStore.bucket` | `S3Bucket` | `newObjectStore` |
| `objectStore.region` | `S3Region` | `newObjectStore` |
| `objectStore.insecure` | `S3Insecure` | `objstore.New` |
| `objectStore.timeout` | `S3Timeout` | `printgw.Timeouts.S3` |
| `objectStore.maxBytes` | `S3MaxBytes` | `printgw.NewService` |
| `objectStore.presignTtl` | `PresignTTL` | `httpapi` presign handler |
| `logging.server` | `LogServer` | `secrets.ResolveLogServer` → `SetLogstashLogger` |

**Correction, caught in review before anything shipped:** the example below sets
`objectStore.endpoint`/`bucket`/`region` to `""` and `logging.server` to a placeholder host. Once
`""`-suppresses-env semantics were implemented (see §3), shipping this example verbatim as the
committed `printservice.config.json` would have silently disabled a working env-configured S3
setup and repointed logstash at a fake host on every deployment that turned the file on. The
committed file instead **omits** these four keys entirely — absence leaves the env var in effect,
which `""` does not. Treat the JSON below as illustrating the *shape* of every key, not as a safe
value to copy for these four specifically.

```json
{
  "version": 1,

  "service": {
    "addr": "127.0.0.1:8090",
    "logLevel": "info"
  },

  "timeouts": {
    "readHeader": "10s",
    "read": "5m",
    "write": "8m",
    "idle": "60s",
    "shutdownGrace": "2m",
    "submit": "30s"
  },

  "limits": {
    "maxHeaderBytes": 65536,
    "maxUploadBytes": 67108864,
    "maxJsonBytes": 8192
  },

  "fetch": {
    "timeout": "60s",
    "maxBytes": 67108864,
    "allowedHosts": ["s3.example.com", "cdn.example.com"]
  },

  "objectStore": {
    "endpoint": "",
    "bucket": "",
    "region": "",
    "insecure": false,
    "timeout": "60s",
    "maxBytes": 67108864,
    "presignTtl": "15m"
  },

  "logging": {
    "server": "logstash.example.com:5044"
  }
}
```

Format rules, each chosen to match how `config.go` already validates:

- **Durations are strings** in `time.ParseDuration` syntax (`"5m"`, `"30s"`). Not integer seconds — the
  existing env vars are already duration strings and the error messages already print durations, so
  keeping one syntax means one parser and one error shape.
- **Byte sizes are JSON integers**, matching `PRINT_GATEWAY_*_BYTES`.
- **`version`** is an integer, currently `1`. A file with an unrecognized `version` is a startup error, so
  a future incompatible re-grouping of keys has somewhere to land.
- **Unknown keys are a startup error** (`json.Decoder.DisallowUnknownFields`). This is the single most
  important rule in the format: silently ignoring `maxUploadBtyes` is precisely the failure class the whole
  of `config.go`'s "malformed value is a startup error, never a silently-ignored override" policy exists to
  prevent — and moving values into a file, where nobody sees an env var conspicuously *not* taking effect,
  makes such a typo far easier to miss than it is today.
- **Every key is optional.** An absent key falls through to the env var, then to the compiled default. An
  empty file `{}` must behave exactly like today's env-only startup — that is the backwards-compatibility
  guarantee, and it is worth a test of its own.

### Excluded, and why

Everything `config.go` knows about that is **not** in the table above, with the reason:

| Value | Why it is not in the file |
|---|---|
| `PRINT_GATEWAY_TOKEN` | Secret. `secrets.ResolveToken` (Vault → env) unchanged. |
| `VAULT_ADDR`, `SECRET_STORE_URL`, `VAULT_TOKEN`, `SECRET_STORE_USERNAME`, `SECRET_STORE_PASSWORD` | Secrets, and the bootstrap needed to *read* secrets. Also injected by the Nomad job spec — putting them in a file would fork the vocabulary every other labOS Go service uses. |
| `PRINT_GATEWAY_S3_ACCESS_KEY` / `_SECRET_KEY` | **Decision reversed, 2026-09-07, by explicit user instruction, after this plan and its implementation initially shipped.** These two ARE now valid in the file (`objectStore.accessKey`/`objectStore.secretKey`), the one deliberate exception to "secrets never go in the file" — for a deployment model where the file itself is rendered from Vault at process start (e.g. a Vault Agent template), not committed or hand-edited. `secrets.ResolveS3Credentials`'s Vault-then-env precedence is unchanged; it now falls back to whichever of env/file supplied a value, and its returned `source` correctly reports `"file"` (or `"file+env"` for a mid-migration mix of the two) when that's what happened (previously hardcoded `"env"`). The tracked `printservice.config.json` was deliberately NOT given example values for these two fields — see `src/printgateway/README.md`'s "Configuration file" section for the file-permission consequence (mode `600`, owned by the `printgateway` service user) and the "Two files, two purposes" convention: a real credential goes in the git-ignored `printservice.config.local.json`, never the tracked example, and `install-services.sh` prefers that file automatically when present. |
| `LABOS_ENV` | Selects the Vault path prefix — part of the secret bootstrap. |
| `PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS` | **Judgment call, flagging it.** `config.go` documents this as existing "at all only because fetch's own tests need to dial `httptest.Server`", and `run` logs it at ERROR level because setting it disables every `file_url` target check. A total SSRF bypass whose only production answer is `false` does not belong in the layer that *wins* over env — leaving it env-only keeps it awkward to set by accident. Say so if you want it in the file anyway; it is a two-line addition. |
| `DefaultMultipartMemoryBytes` | Already deliberately non-configurable — an internal memory/disk tradeoff, per its own doc comment. Not an env var today either. |
| The config file's own path | Chicken-and-egg; see §4. |

Note the asymmetry `objectStore` creates, and accept it deliberately: `endpoint`/`bucket`/`region` are in
the file, but the credentials for that same endpoint are not. That is correct — it is the same split
`config.go` already documents, where `S3Endpoint` is plain config while the keys route through `secrets`.

---

## 3. Precedence, and the provenance problem

Resolution order per value:

```
1. printservice.config.json    (wins)
2. PRINT_GATEWAY_* env var
3. compiled-in default
```

Two consequences that need real work, not just an `if`:

**(a) Absent must be distinguishable from zero.** The file struct's scalars therefore become pointers
(`*string`, `*int64`, `*bool`, and `*string` for durations), or the file is decoded into
`map[string]json.RawMessage` per group. Pointers are simpler and keep the `DisallowUnknownFields` check.
Without this, `"maxUploadBytes": 0` in the file would read as "unset, use the env var" instead of as the
error it is, and `"insecure": false` would be indistinguishable from omitting it.

**(b) Error and log messages must name the source that actually supplied the value.** Today every message
names an env var:

```
PRINT_GATEWAY_WRITE_TIMEOUT (6m) must exceed PRINT_GATEWAY_READ_TIMEOUT+max(...)+... (6m30s)
```

If those values came from the JSON file, that message sends the operator hunting an env var they never set.
So `Load` must track, per field, where each value came from, and render it — e.g.
`write timeout (6m, from printservice.config.json:timeouts.write) must exceed …`. Concretely: a
`map[string]string` (field → human-readable source) built as each value is resolved, plus a small helper
used in place of the bare `…Env` constants inside `validate` and inside the startup log lines in `main.go`
that currently print `config.FetchAllowedHostsEnv`, `config.S3EndpointEnv`, `config.S3BucketEnv`,
`config.S3RegionEnv` and `config.LogLevelEnv`. This is the largest single piece of the change and the one
most likely to be under-estimated.

**Unchanged:** `validate(cfg)` runs on the *merged* `Config`, so the `WriteTimeout > ReadTimeout +
max(Fetch,S3) + Submit` and `ShutdownGrace > max(Fetch,S3) + Submit` budget checks keep firing regardless of
which layer supplied each term. That is the whole reason to merge first and validate once, rather than
validating the file in isolation.

---

## 4. Where the file comes from

**SUPERSEDED — this section's default-path and `-config`-flag design was deliberately NOT built.**
What actually shipped: discovery is **explicit-only**. The file is read if and only if
`PRINT_GATEWAY_CONFIG` names its path — no default path (`/etc/printgateway/...` or "beside the
binary"), no probing, and no `-config` CLI flag. Reason: `tests/profiles.json` drives profiles A-G
purely by env and `startup-matrix.sh` launches under `env -i`; any implicit default path or a
flag would risk silently overriding that harness. If the file is named and is missing, unreadable,
or malformed, startup fails naming both `PRINT_GATEWAY_CONFIG` and the path — there is no "start
normally, log one INFO line saying no config file was found" fallback for a *named* path (that
behavior only applies when the env var itself is unset, which needs no INFO line at all — the
service behaves exactly as it did before this feature existed). See
`src/printgateway/README.md`'s "Configuration file" section for the accurate operator-facing
description, and the "Locked decisions" table in
`C:\Users\roy.r\.claude\plans\ok-so-now-create-cached-squid.md` for the reasoning.

`Load`'s signature grew a file reader, injected the same way `getenv` already is, so tests never touch the
real filesystem:

```go
func Load(args []string, getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error)
```

`main.go` passes `os.ReadFile`; `run` gained the same parameter. The existing positional-argv
convention (`args[1]` is an address override) was kept exactly as-is, with **no `-config` flag
added** — the address precedence is `argv → service.addr (file) → default`, argv always winning
even over the file, so a config file can never silently override an address passed deliberately on
the command line. Whether to add a `-config` flag remains an explicitly open, non-blocking
question (unchanged from this plan's original framing) — nothing in what shipped precludes adding
one later.

---

## 5. Files touched

| File | Change |
|---|---|
| `printservice.config.json` | Replace the leftover OpenAPI body with the §2 config document. |
| `src/printgateway/internal/config/config.go` | New `fileConfig` struct (pointer fields), decode with `DisallowUnknownFields`, merge into `Config`, provenance map, `Load` signature. |
| `src/printgateway/internal/config/config_test.go` | file-wins-over-env; env-fills-gaps; empty `{}` equals today; unknown key rejected; bad duration rejected; zero value rejected; missing explicit path fails; missing default path succeeds; `validate` errors name the file. |
| `src/printgateway/cmd/printgateway/main.go` | Thread `readFile` through `run`; swap the `…Env` constants in the startup log lines for provenance-aware source names. |
| `src/printgateway/cmd/printgateway/main_test.go` | Fake `readFile`; assert the no-config-file INFO line and a file-driven startup. |
| `src/printgateway/README.md` | New "Configuration" section: the file, the precedence, the excluded table. The existing env-var docs stay — they are still layer 2. |
| `src/printgateway/Dockerfile`, `docker-entrypoint.sh` | Decide where the file is mounted; wire `PRINT_GATEWAY_CONFIG` if a non-default path is used. |
| `docs/STATUS.md` | Log the phase when it completes, per the repo's own rule. |

---

## 6. Suggested staging

**SUPERSEDED — this section's staging was not the staging actually followed.** What was actually
built used a different, 5-commit staging designed to keep every intermediate commit
behavior-preserving and separately reviewable (full detail in
`C:\Users\roy.r\.claude\plans\ok-so-now-create-cached-squid.md`, §4 "Staging"):

1. **Behavior-preserving refactor** — extract the parse/validate halves of the existing
   env-override helpers (durations, byte sizes, host-list normalization) so a second source can
   share them, with zero test changes and zero behavior change (verified: the pre-existing test
   suite passes untouched).
2. **Inert provenance infrastructure** — the `sources` map, `Config.Source`, `FileSourcedKeys`,
   `AddrSource`, `ConfigFilePath`, wired into `validate`/`main.go`/`internal/secrets`'s messages —
   added before any file layer exists, so every message stays provably byte-identical (`sources`
   is always empty at this point).
3. **The file layer itself** — `fileConfig`, `ConfigPathEnv`, `decodeFileConfig`, the version
   check, the merge logic, `Load`/`run`'s signature change, the required startup INFO line, and
   the full Go test matrix (config_test.go + main_test.go) — done as one reviewed unit rather
   than split into "decode" and "merge" separately, since a valid-but-unmerged file is a
   dangerous half-state.
4. **Data and docs** — this stage: `printservice.config.json` rewritten with the §2 format
   (deliberately omitting the four env-suppressible keys — `objectStore.endpoint`/`bucket`/
   `region` and `logging.server` — rather than setting them to `""`, so the committed example is
   behavior-identical to today's defaults; see the correction note on §2's example above),
   `README.md`'s "Configuration file" section, this document's §4/§6 corrections, and the
   `docs/STATUS.md` entry.
5. **Deployment** — Docker/systemd mounting notes, deliberately not baking `PRINT_GATEWAY_CONFIG`
   into either by default (a bind-mounted file that stops being mounted turns a healthy service
   into a startup failure).

A JSON Schema (`printservice.config.schema.json`) remains a genuinely deferred follow-up, not part
of any stage above — case-insensitive key matching and a duplicate-group-merges-not-discards
quirk (see §3's provenance note; verified live) are both accepted gaps until that pass happens.

**Resolved, not left open:** `allowPrivateTargets` stays env-only, permanently — see §2's excluded
table. It is now enforced at the type level: there is no field for it anywhere in the Go struct
the file decodes into, so naming it in the JSON is rejected as an unknown field rather than merely
being undocumented.

---

## 7. Second reshape (2026-09-08): the labOS `resource/*` convention

**Decision, by explicit user instruction:** the flat, bespoke shape §2 describes (`service`,
`timeouts`, `limits`, `fetch`, `objectStore`, `logging` as top-level siblings) was replaced with
the same `resource/*` convention other labOS services already use for their own config files —
provided as a reference file from a real `OperationsService` deployment, shaped like:

```json
{
  "resource/log": { "format": "json", "host": "10.0.1.186", "port": "514", "type": "LOG4CXX" },
  "resource/service_discovery": { "url": "10.0.1.112:8500", "type": "consul" },
  "resource/cache": { "type": "redis", "host": "127.0.0.1", "port": "6379" },
  "resource/file_storage": { "host": "s3.eu-west-1.amazonaws.com", "s3-user": "...", "s3-password": "..." },
  "resource/controlplane": { "...OperationsService-specific settings..." }
}
```

The goal was to match this "as closely as possible", adjusted from OperationsService's own
content to printgateway's. Three structural decisions were made explicitly (asked and answered
before implementing):

1. **No top-level `version` key.** The reference format has none, and this reshape drops the
   version-check mechanism entirely (previously a startup error on any value other than `1`) to
   match it exactly, rather than keeping `version` as an extra key the convention doesn't have.
   There is now no future-compat gate at all — an incompatible future re-shaping of this file has
   no version bump to land on. Accepted tradeoff, not an oversight.
2. **`resource/service_discovery` (Consul) and `resource/cache` (Redis) are omitted entirely.**
   printgateway uses neither. There is no Go field for either anywhere in the type tree that
   decodes this file — naming them is an "unknown field" startup error, the same enforcement
   `allowPrivateTargets` already gets, not an inert placeholder.
3. **Bucket/region and every other printgateway-specific S3 setting live under
   `resource/printgateway.objectStore`, NOT under `resource/file_storage`.** The reference's own
   `resource/file_storage` carries only `host`/`s3-user`/`s3-password` — genuinely shared,
   cross-cutting S3 *connection* infrastructure — while which bucket a given consumer uses is that
   consumer's own concern (OperationsService's reference file picks its bucket via
   `resource/controlplane.fileBackupStore.path`, not via `resource/file_storage`). printgateway
   follows the same split: `resource/file_storage` stays minimal and shared; bucket, region,
   insecure, timeout, maxBytes, and presignTtl are printgateway's own usage details of that shared
   resource, so they live under `resource/printgateway.objectStore` instead.

### The actual shape (supersedes §2's JSON example)

```json
{
  "resource/log": {
    "host": "logstash.internal",
    "port": "514"
  },
  "resource/file_storage": {
    "host": "s3.eu-west-1.amazonaws.com",
    "s3-user": "AKIA...",
    "s3-password": "..."
  },
  "resource/printgateway": {
    "addr": "127.0.0.1:8090",
    "logLevel": "info",
    "timeouts": {
      "readHeader": "10s", "read": "5m", "write": "8m",
      "idle": "60s", "shutdownGrace": "2m", "submit": "30s"
    },
    "limits": { "maxHeaderBytes": 65536, "maxUploadBytes": 67108864, "maxJsonBytes": 8192 },
    "fetch": { "timeout": "60s", "maxBytes": 67108864, "allowedHosts": ["s3.example.com"] },
    "objectStore": {
      "bucket": "my-bucket", "region": "eu-west-1", "insecure": false,
      "timeout": "60s", "maxBytes": 67108864, "presignTtl": "15m"
    }
  }
}
```

### Key-by-key mapping from §2's old table

| Old (flat) | New (`resource/*`) |
|---|---|
| `service.addr` | `resource/printgateway.addr` |
| `service.logLevel` | `resource/printgateway.logLevel` |
| `timeouts.*` | `resource/printgateway.timeouts.*` (unchanged sub-keys) |
| `limits.*` | `resource/printgateway.limits.*` (unchanged sub-keys) |
| `fetch.timeout` / `fetch.maxBytes` / `fetch.allowedHosts` | `resource/printgateway.fetch.*` (unchanged sub-keys) |
| `objectStore.bucket` / `.region` / `.insecure` / `.timeout` / `.maxBytes` / `.presignTtl` | `resource/printgateway.objectStore.*` (unchanged sub-keys — these stayed put; only endpoint/credentials moved out) |
| `objectStore.endpoint` | **`resource/file_storage.host`** (moved to the shared block) |
| `objectStore.accessKey` | **`resource/file_storage.s3-user`** (moved, kebab-case key) |
| `objectStore.secretKey` | **`resource/file_storage.s3-password`** (moved, kebab-case key) |
| `logging.server` (one `"host:port"` string) | **`resource/log.host` + `resource/log.port`** (split into two string fields — `Config.LogServer` is still one combined `"host:port"` string internally; `mergeFileConfig` combines the two file fields into it) |
| `version` | **removed, no replacement** |

The `""`-suppresses-env / `service.logLevel`-is-the-one-exception rules from §2/§3 are otherwise
unchanged in spirit, just relocated to their new paths. `resource/log`'s split fields add one new
rule: if either `host` or `port` is present, `LogServer` becomes their combination; if BOTH
resolve to empty string, `LogServer` is explicitly suppressed to `""` (an operator blanking both
fields deliberately disables logstash shipping via the file, the same "blank means off" pattern
every other suppressible field already has) — read `mergeFileConfig` in `config.go` for the exact
current logic rather than trusting this summary as version-pinned.

Every file this reshape touched: `internal/config/config.go` (the `fileConfig`/`fileLog`/
`fileFileStorage`/`filePrintgateway` type tree and `mergeFileConfig`, rewritten), its test files
(`config_test.go`, `secrets_test.go`, `cmd/printgateway/main_test.go` — every JSON literal
updated to the new paths, the version-handling test removed, a new test added for `resource/log`'s
host+port combination), `printservice.config.json` (tracked, secret-free — omits `resource/log`
and `resource/file_storage` entirely, same "absence leaves env in effect" principle as before) and
`printservice.config.local.json` (git-ignored, the operator's real per-deployment file — see
`README.md`'s "Two files, two purposes"), and `README.md` itself.
