# printerSearch/HDL — status (last updated 2026-07-22)

Continuation of LAB-16894 under `C:\printerSearch\HDL`. The original POC in
`C:\printerSearch` (outside HDL) is **frozen as a backup — do not modify it**.
Everything below is new work built on top of it.

## What's here

- `print-server-spec.docx` — full spec (איפיון) for the production Print
  Gateway/Worker service (source: `build_spec.js`).
- `cups-vs-spooler-comparison.docx` / `cups-vs-spooler-comparison-en.docx` —
  performance comparison, CUPS+IPP vs. Windows Spooler+SumatraPDF, backed by
  real measurements (sources: `build_comparison.js` / `build_comparison_en.js`).
- `WSL/` — the working Go code (copied from the old POC, `ippfix` rebuilt) and
  all the test/benchmark tooling. See below for what's running there.
- `win-bench/` — Windows-side benchmark (`win-bench.ps1`) plus the file-backed
  test printer setup.

## Environment: CUPS now runs natively in WSL, not Docker

Per explicit decision, this phase moved off the Docker container entirely.
CUPS runs directly inside a **WSL2 `Ubuntu-24.04`** distro (`wsl -d Ubuntu-24.04`),
deliberately the strictest available cups-filters version (2.0.0) to
stress-test the fix. The old `cups-poc` Docker container was removed (no
longer needed/relevant).

**How to resume after a Windows/WSL restart** — run these inside
`wsl -d Ubuntu-24.04 -u root`:

1. Check services are up: `systemctl is-active cups ippfix` — both should
   already auto-start (enabled), no manual restart needed unlike the old
   Docker setup.
2. Verify queues survived: `lpstat -p` should list ~19 queues: `brother-fixed`
   (old POC's queue, untouched), `vp1`/`vp2`/`vp3` (early 3-printer test), and
   15 `q-*` queues (the 15-printer/700-job test fleet — see below). If any
   are missing, re-run `bash /mnt/c/printerSearch/HDL/WSL/setup-cups-queues-for-emulators.sh`
   and `bash /mnt/c/printerSearch/HDL/WSL/setup-15-queues.sh`.
3. Verify the 15 virtual printers are up: `systemctl is-active ippeve-hp-laserjet
   ippeve-canon-ir ippeve-xerox-versalink ippeve-epson-wf ippeve-kyocera-ecosys
   ippeve-ricoh-mp ippeve-lexmark-mx ippeve-konica-bizhub ippeve-sharp-mx
   ippeve-dell-smart ippeve-brother-hl ippeve-samsung-proxpress ippeve-zebra-zt
   ippeve-star-tsp ippeve-oki-b432` (all `enabled`, auto-start). If missing,
   re-run `bash /mnt/c/printerSearch/HDL/WSL/setup-15-printers.sh`.
4. From Windows, `curl http://localhost:631/` should return CUPS's web UI —
   confirms WSL2 localhost-forwarding is working (no port conflict, since the
   old Docker container is gone).

## Known environment gotchas fixed this phase

- **CUPS was socket/path-activated by default** (`cups.socket`/`cups.path`),
  which combined with `IdleExitTimeout` caused it to silently restart between
  commands and lose any `lpadmin`-added queue that hadn't been flushed to disk
  yet. Fixed by disabling `cups.socket`/`cups.path` and running `cups.service`
  as a plain always-on service (`systemctl disable --now cups.socket cups.path`).
  If queues start disappearing again after a fresh WSL install/reboot, check
  `systemctl is-enabled cups.socket cups.path` — should both say `disabled`.
- **`MaxJobs` default (500)** caused real "Too many active jobs" rejections
  under the 700-job load test (jobs stay "active" a long time because the
  virtual printers simulate realistic mechanical print speed). Raised to 2000
  in `/etc/cups/cupsd.conf`. Relevant for real production capacity planning,
  not just this test rig.
- Git Bash/MSYS mangles `/mnt/c/...`-style paths passed through `wsl.exe` —
  prefix commands with `MSYS_NO_PATHCONV=1` (same gotcha as the original POC
  had with Docker paths).
- `wsl.exe -d Ubuntu-24.04 -u root -- bash -c "... & ..."` does **not** keep
  background (`&`) processes alive after the invoking command returns — use a
  **systemd service** (see `ippfix.service`, `ippeve-*.service` units) instead
  of bare `nohup`/`&`, which is what all the long-running pieces here use.

## `ippfix` — rebuilt as a template-overlay proxy (not a single-field patch)

`WSL/ippfix/main.go` is a new, more general version of the original POC's
proxy. Instead of hardcoding "replace empty naturalLanguage with en-us", it:

1. Captures a full "golden" snapshot of the printer's real
   `Get-Printer-Attributes` response once (`-gen-template`), with known
   tag-level fixes applied (`WSL/ippfix/printer-template.json`, 86 attributes).
2. At request time, applies the same tag-level fix (empty naturalLanguage/
   charset → sane default) to the **entire** message, not just one group —
   the first version of this session's rewrite only fixed the
   printer-attributes group and that alone wasn't enough, because the broken
   field also appears in the response's own operation-attributes group, which
   is what a strict client checks first.
3. Additionally overlays the printer-attributes group attribute-by-attribute
   against the template: real value wins if present/valid, template fills any
   gap (empty or entirely missing attribute).

**Confirmed working**: `cups-filters 2.0.0`'s `driverless` PPD generator,
which previously hard-failed against this printer's raw malformed response
(this was independently re-confirmed from the original session's history),
now succeeds cleanly through this proxy. Bonus, untested-on-hardware finding:
2.0.0 also seems to have fixed the old 1.28.17 `image/urf` margin bug (the
`pwg-raster` PPD edit wasn't needed to get a clean raster conversion this
time) — worth re-verifying against the real printer before relying on it.

Run as systemd service `ippfix.service` (survives reboots on its own, unlike
the old Docker `docker exec -d` approach).

## Load-testing rig built this phase (all in `WSL/`)

- `printersearch bench` — new subcommand on the existing Go IPP client
  (`bench.go`). Submits N jobs at configurable concurrency across multiple
  ports/CUPS-queue-paths; reports latency percentiles + throughput per target
  and overall. `-wait-completion` additionally polls `Get-Job-Attributes`
  until job-state is terminal, to separately measure *acceptance* latency vs.
  *completion* latency (the latter is dominated by the virtual printers'
  simulated mechanical print speed — not a fair measure of software cost, see
  the comparison doc for why).
- `setup-emulators.sh` / `setup-cups-queues-for-emulators.sh` — the original
  3-printer (`vp1`/`vp2`/`vp3`) test fleet.
- `setup-15-printers.sh` / `setup-15-queues.sh` — the 15-printer fleet used
  for the 700-job test (10 declaring direct `application/pdf` support, 5 not
  — forcing real local Ghostscript rendering on those 5). Printer/vendor
  names are cosmetic labels on the same `ippeveprinter` binary, not real
  vendor-specific emulation.
- `cups-resource-bench.sh` — wraps `printersearch bench`, sampling `cupsd`'s
  own CPU/RSS plus any Ghostscript/filter child processes it spawns, every
  ~10ms, for CPU/memory reporting alongside latency.
- `win-bench/win-bench.ps1` — the Windows-side equivalent: submits N jobs via
  `SumatraPDF -print-to` against a **file-backed local printer**
  (`BenchFilePrinter`, port = a literal file path, driver = real
  "Brother MFC-L2700DW series") so it exercises the real spooler+driver
  pipeline without prompting for a save path or wasting paper. Samples
  `spoolsv.exe`/`SumatraPDF.exe` CPU/memory inline (a separate `Start-Job`
  monitor was tried first and didn't work — its own startup lag meant it
  never got a sample in before short runs finished).

## Headline result (full detail + caveats in the comparison docs)

CUPS+IPP's client-facing accept latency is **~54x faster** than
`SumatraPDF -print-to` (~62ms vs. ~3.36s avg/job, 700 vs. 60 jobs, same file),
because CUPS returns as soon as a job is queued and renders asynchronously in
the background, while SumatraPDF blocks synchronously until its own
rendering + spooler handoff finishes. Ghostscript is also ~5x cheaper in CPU
and ~17x cheaper in peak concurrent memory than SumatraPDF per render. This
is real evidence for the still-open LAB-16894 SumatraPDF-vs-CUPS/IPP
decision — see `project_lab16894_printing.md` memory for how to apply it.

## Third phase (2026-07-27): HA/queue/security spec extension

Added a standalone spec document, `print-server-spec-ha.docx` (source:
`build_spec_ha.js`). Contains ONLY the new HA/queue/security material as
its own 9-section document (not combined with the original part 1) — an
earlier combined version (`print-server-spec-v2.docx`/`build_spec_v2.js`)
was tried first but the user explicitly said they didn't want the
combination, just the new material standalone; that combined version was
removed (`build_spec_v2.js` deleted; `print-server-spec-v2.docx` should be
deleted too once it's not open in Word anymore — it was locked at the time).
The original `print-server-spec.docx`/`build_spec.js` were left untouched
throughout, per explicit user instruction not to overwrite existing files.

**Bidi/RTL fix (2026-07-27, same day) — took two attempts**: the user
reported visible Hebrew/English jumbling in `print-server-spec-ha.docx`
(Word's bidi algorithm reorders neutral characters — hyphens, slashes,
parens, digits — unpredictably when English/technical terms sit in a
Hebrew RTL paragraph with no explicit direction marker).

- First attempt: wrapped every Latin/digit/technical run in Unicode bidi
  isolate marks (LRI U+2066 / PDI U+2069) via a `fixBidi()` helper. This
  **backfired** — the user's Word rendered these as visible boxes reading
  literally "LRI"/"PDI", making the document look worse, not better.
- Fixed properly by replacing that entire approach: `build_spec_ha.js` now
  has `splitBidiSegments()`/`Runs()`, which splits mixed text into
  alternating Hebrew/Latin **segments** and puts each Latin segment in its
  own `TextRun` with the OOXML run-level property `rightToLeft: false`
  (`<w:rtl w:val="false"/>` in `w:rPr`) — pure formatting metadata, no
  characters inserted into the text at all, so nothing can render visibly.
  Verified directly in `word/document.xml`: zero U+2066/U+2069 codepoints
  anywhere in the document, 891 `<w:rtl` run-property occurrences, and the
  English spans (e.g. `(CUPS + IPP + ippfix)`) sit in their own run
  immediately after the Hebrew run, exactly as intended.

**User explicitly declined to apply this fix to the original
`print-server-spec.docx`/`build_spec.js` or to the
`cups-vs-spooler-comparison(-en).docx` docs** — those likely have the same
underlying bidi issue (same unfixed helper functions) but are intentionally
left as-is; only touch them if the user asks later. If reused elsewhere,
copy the `Runs()`/`splitBidiSegments()` approach, not the LRI/PDI one.
Covers, based on real web research (not invented): N+1 gateway/worker
redundancy with two separate HA layers (CUPS-level `cups-browsed`/
`implicitclass` vs. job-queue-level broker HA), shared-queue technology
choice (NATS JetStream recommended over RabbitMQ quorum queues/Kafka/SQS,
with reasoning and a Shopify-vs-Cloudflare real-world contrast), REST vs.
direct-queue-exposure intake, file-delivery refinement (multipart default,
presigned-URL/MinIO above ~10MB), audit-trail schema + tamper-evidence
(hash-chaining, append-only, WORM) separate from Kibana/ELK operational
logs with W3C Trace Context correlation, service-to-service security
(mTLS default, Ghostscript/container hardening incl. real CVEs, SSRF
mitigation), a leader-election decision rule (not needed — stateless
consumers + idempotency + visibility-timeout suffice), the physical
duplicate-print risk on crash-retry, capacity/resource budget estimates,
and an explicit "gaps I added" section (DB-itself-needs-HA, printer-catalog
distribution, per-caller rate limiting, per-printer health checks, NTP) plus
a failure-mode table.

## Fourth deliverable (2026-07-27): standalone HLD-foundation conclusions doc

`print-gateway-hld-foundation.docx` (source: `build_hld_foundation.js`) — a
brand-new, fully self-contained 16-section document with NO references to
`print-server-spec.docx` or `print-server-spec-ha.docx`. Per explicit user
request: everything already decided is written as a flat conclusion (no
"option 1/2" framing); only 3 genuinely-still-open decisions keep the
options+recommendation format (sync-vs-queue intake, REST-only-vs-expose-
queue, direct-attach-vs-URL file delivery, and — new — Kibana-vs-Prometheus
for exposing status). Built from scratch using the `hebrew-docx-bidi` skill
pattern (`Runs()`/`splitBidiSegments()`) from the start, not retrofitted —
verified clean (zero stray Unicode bidi control chars in the actual
content, only one legitimate library-internal RLM inside the TOC field
code). New section 15 (not in either prior doc) covers CUPS's built-in web
GUI (port 631): what it shows, its real limitations for a multi-node
architecture (no cross-node aggregation, localhost-only by default,
PAM/basic-auth only — no RBAC, industry-treated as a per-node debug tool
not a stakeholder dashboard — confirmed via research, incl. Microsoft
Universal Print/PaperCut both building their own centralized dashboards
rather than exposing per-node UIs), and the recommended answer instead: a
lightweight internal status view in the Gateway service reading from the
shared job-store DB, plus a still-open choice between a Kibana dashboard
(reuse existing ops-log pipeline, zero new infra) or Prometheus/Grafana
(if already standardized elsewhere in the org) — mentions the real,
maintained `phin1x/cups_exporter` OSS project as a building block if
Prometheus is chosen, but notes it only sees one node's local view.

## English translation (2026-07-27, same day)

`print-gateway-hld-foundation-en.docx` (source: `build_hld_foundation_en.js`)
— full English translation of the HLD-foundation doc, same 16 sections and
structure. Plain LTR document (no bidi helper needed — all-English content).

## Fifth phase (2026-08-24): printgateway dry e2e test against the real printer

The WSL environment described above was **gone** at the start of this session — the
distro is actually named `Ubuntu` (not `Ubuntu-24.04`; update any docs/scripts that
assume that name), and it had no `cups`/`cups-filters`/`ippfix`/queues installed at all,
just the bare distro. Everything below was rebuilt from scratch this session:

- Installed `cups cups-filters cups-client` (pulled in cups-filters **2.0.0**, matching
  what this phase's doc already assumed). Set `cups.service` up the documented way:
  `systemctl disable cups.socket cups.path` (do **not** `mask` them — `cups.service` has
  `Requires=cups.socket`, so masking makes the service fail to start entirely; disabling
  is sufficient since it's started directly via `WantedBy=multi-user.target`, not pulled
  in by the socket).
- **Discovered a real bug in `ippfix`'s template-overlay logic**: the tag-level
  naturalLanguage/charset fix (`fixEmptyRequiredTags`) is only ever invoked from inside
  `applyTemplate`, and `main()` only calls `applyTemplate` when a template was loaded
  AND (implicitly, per the handler) — worse, when a client sends a filtered
  `requested-attributes` Get-Printer-Attributes (which the CUPS `ipp` backend does routinely,
  as opposed to the unfiltered "get everything" request `driverless`/`printersearch info`
  send), the response `ippfix` produces comes back as IPP `server-error-internal-error`,
  which the backend treats as fatal and the job never leaves "printing" state. Root cause
  not yet fixed in code — confirmed by capturing the WSL debug log
  (`LogLevel debug` in `/etc/cups/cupsd.conf`) and comparing a queue that points at
  `ippfix` (`brother-fixed`, broken this way) against one pointed straight at the printer
  (`brother-direct`, works). **Needs a real fix before `ippfix` is used for anything beyond
  one-time `driverless`-PPD-generation.**
- Because of that, generated the static PPD once through `ippfix` (still needed — the
  naturalLanguage bug does break `driverless` capability negotiation) using the
  already-committed `printer-template.json`, then pointed the actual print queue
  (`brother-direct`) **directly at the printer**, skipping `ippfix` for real traffic.
  This is a valid variant of the documented approach: `ippfix` is a one-time PPD-generation
  aid here, not a persistent runtime proxy, until its bug above is fixed.
- **Resolved the open item below**: with cups-filters 2.0.0's default `image/urf` PPD
  (the `pwg-raster` `cupsFilter2` edit from the old 1.28.17 setup was deliberately left
  untouched), a real 2-page `printDemo.pdf` printed correctly on the physical
  Brother MFC-L2700DW via the `brother-direct` queue — confirmed by the user. The
  1.28.17 margin bug is confirmed fixed in 2.0.0 on real hardware, not just the emulator.
- Built `printgateway` for linux, ran it as a systemd unit (`printgateway.service`) with
  `PRINT_GATEWAY_TOKEN` set in the unit's `Environment=`, and did a real
  `curl -X POST http://127.0.0.1:8090/print -H 'X-Labos-Print-Token: ...' -F printer=brother-direct -F file=@printDemo.pdf`
  against it — **got back `{"status":"submitted",...}`, and the job printed correctly
  on the real printer**, confirming the full HTTP-gateway-to-paper path works, not just
  the raw CUPS path validated in earlier phases.
- Gotcha hit and worth recording: WSL2's `/tmp` is tmpfs and gets wiped whenever the
  lightweight VM restarts after its idle timeout (independent of the distro/services
  themselves, which persist via systemd) — a PPD and test PDF placed in `/tmp` vanished
  mid-session. Put anything meant to survive under a disk-backed path (this session used
  `/opt/printgw/`), not `/tmp`.

## Open items / not yet done

- None of this is wired into an actual Print Gateway/Worker service yet —
  it's still POC/benchmark-quality code proving the CUPS+IPP path's
  viability and performance, per the spec doc.
- `ippfix`'s template-overlay path is broken for filtered `requested-attributes`
  requests (see fifth-phase entry above) — fine for one-shot `driverless` PPD
  generation, not safe yet as a persistent runtime proxy in front of real print traffic.
- No printer-catalog/onboarding automation yet (same open item as the
  original POC) — the 15-printer test fleet is hand-built, not a reusable
  onboarding pipeline.
