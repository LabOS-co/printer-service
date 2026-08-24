# printerSearch — status (last updated 2026-07-19)

POC for LAB-16894 (centralized Windows printing service): print a PDF to a network printer
(Brother MFC-L2700DW, 192.168.252.210) via CUPS+IPP, no Windows print spooler/SumatraPDF in
the main path. **Result: working end-to-end**, validated with real physical prints.

## What's in this directory

- `main.go`, `ipp.go`, `go.mod`, `printersearch.exe` — Go IPP client (raw stdlib, no 3rd-party
  IPP lib). Subcommands: `info` (Get-Printer-Attributes), `print` (Print-Job with explicit
  media/resolution/color/page-range job attributes), `jobs` (Get-Jobs), `cancel` (Cancel-Job).
- `docker/` — Dockerfile + entrypoint.sh for the `cups-poc` container: Debian bookworm,
  cups + cups-filters **1.28** (deliberately not 2.0.0 — see gotchas below).
- `ippfix/` — Go reverse proxy (`main.go`, cross-compiled to `ippfix-linux`) that sits between
  CUPS and the real printer and patches the printer's firmware bug: naturalLanguage-tagged IPP
  attributes (`attributes-natural-language` etc.) come back empty, violating RFC 8011 §5.1.9,
  which breaks driverless negotiation. The proxy rewrites only that; everything else passes
  through byte-for-byte.
- `win/` — control-test tool: prints via the normal Windows spooler (shells out to SumatraPDF's
  `-print-to`) to compare against the CUPS/IPP path. Not part of the main deliverable.
- `printDemo.pdf` — 2-page A4 test file.

## Root cause chain (fully diagnosed)

1. Printer's IPP Get-Printer-Attributes response has a firmware bug: naturalLanguage fields are
   zero-length (confirmed independently by our parser and CUPS's own IPP client).
2. That breaks **generic/driverless** IPP negotiation specifically — both CUPS `-m everywhere`
   and Windows' generic "Microsoft IPP Class Driver" mis-negotiate against it (symptom:
   continuous blank-page printing, `media-needed-error`/`spool-area-full-report`). Proven NOT a
   printer hardware fault: Windows spooler + SumatraPDF + the real vendor-specific "Brother
   MFC-L2700DW series" driver, over the same IPP port 631, printed perfectly.
3. Fix for CUPS: don't let it live-query the broken printer. `ippfix` patches the bug, which lets
   CUPS's own driverless PPD generator (`/usr/lib/cups/driver/driverless cat ipp://127.0.0.1:6310/ipp/print`)
   succeed and produce a complete, correct PPD — install that as a **static** PPD queue instead
   of relying on live `-m everywhere` negotiation on every print.
4. One more cups-filters 1.28.17 bug: the generated PPD's `cupsFilter2` defaults to `image/urf`,
   and that filter chain (`gstoraster`→`rastertopwg`) has a margin-computation bug (integer
   underflow → "Unsupported raster data"), independent of page size. Fix: edit the PPD's
   `cupsFilter2` line to target `image/pwg-raster` instead (this printer accepts both natively).
5. Also hit and fixed: the IPP job attribute for resolution is `printer-resolution`, not
   `print-resolution` (RFC 8011) — wrong name is silently ignored, Ghostscript falls back to
   600dpi/8-bit-grayscale and overflows the printer's spool.

With all of the above, a real print through the actual `cupsd` (not just `cupsfilter` CLI
dry-runs) succeeded twice: correct page count (2, matches source PDF), A4, 300dpi, confirmed
physically printed correctly by the user (2026-07-16 and again 2026-07-19).

## How to resume after a restart

The `cups-poc` container and its CUPS queues (`brother` = plain driverless, `brother-fixed` =
the working static-PPD queue) persist across container restarts, but:

- **`ippfix` does NOT persist** — it was started with `docker exec -d`, not part of the
  container's own startup. After any container restart, re-run:
  ```
  docker --context desktop-linux exec -d cups-poc /usr/local/bin/ippfix -listen :6310 -target http://192.168.252.210:631
  ```
- Verify the container is up: `docker --context desktop-linux ps -a --filter name=cups-poc`
  (start it with `docker --context desktop-linux start cups-poc` if not running).
- Print via the working queue:
  ```
  ./printersearch.exe print -host localhost -path /printers/brother-fixed -file printDemo.pdf -resolution 300
  ```
- Check printer state without printing: `./printersearch.exe info -host 192.168.252.210`

## Known environment gotchas (this machine)

- **Docker Desktop crashes intermittently** with "initializing Inference manager... The file
  cannot be accessed by the system" on `dockerInference`/`userAnalyticsOtlpHttp.sock` under
  `%LOCALAPPDATA%\Docker\run\`. Fix: kill all Docker Desktop processes (leave
  `com.docker.service`/`dockerd.exe` running), `Rename-Item` (not delete — delete fails too) the
  `run` directory to something else, relaunch Docker Desktop. Happened twice this session.
- Docker Desktop can be in **Windows containers** or **Linux containers** mode — this project
  needs Linux (`--context desktop-linux`). If `docker --context desktop-linux ps` errors with
  "server supports the requested API version" or similar, containers mode is probably set to
  Windows — switch it in Docker Desktop settings.
- Git Bash/MSYS mangles paths that look like absolute Unix paths (e.g. `/printers/brother-fixed`
  in a `docker exec` arg) — prefix commands with `MSYS_NO_PATHCONV=1` when this matters.
- Upgrading the container to Ubuntu 24.04 + cups-filters 2.0.0 made things *worse*, not better —
  it enforces stricter RFC validation and refuses to even create the driverless queue against
  this printer's malformed response. Stay on Debian bookworm + cups-filters 1.28.x.

## Open items / not yet done

- Code is POC-quality, not integrated into whatever the real Print Gateway/Worker service
  ends up being (LAB-16894 Story 1 HLD still needs the Queue-vs-REST-API decision separately).
- `ippfix` currently only patches the one known naturalLanguage bug class — a different printer
  model with a different malformed-response bug would need the proxy extended, following the
  same diagnostic process (dump raw attributes via `printersearch info`, compare to RFC, patch).
- No catalog/reuse mechanism yet for static PPDs across multiple printer models — worth
  building if the service needs to support many printers.
