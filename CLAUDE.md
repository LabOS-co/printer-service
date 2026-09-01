# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

POC/prototype work for **LAB-16894** — a centralized print service that prints PDFs to network
printers via **CUPS + IPP**, bypassing the Windows print spooler and SumatraPDF. Not a product:
it is a proven end-to-end path (real physical prints), a benchmark rig, a set of Hebrew/English
spec + HLD documents, and a first Print Gateway HTTP prototype.

Three Go modules plus one Node module, no shared workspace — each has its own `go.mod`/`package.json`
and is built independently. There is no test suite, no linter config, and no git repository here.

### The two generations

- **Root (`main.go`, `ipp.go`, `ippfix/`, `docker/`, `win/`)** — the original Docker+CUPS POC.
  `HLD/STATUS.md` says this is **frozen as a backup — do not modify it.** Read it for history;
  make changes under `HLD/`.
- **`HLD/`** — all current work. CUPS runs natively in a WSL2 `Ubuntu-24.04` distro (the Docker
  `cups-poc` container is gone), `ippfix` was rewritten as a template-overlay proxy, and the
  benchmark/load-test rig and the Print Gateway prototype live here.

`STATUS.md` (root) and `HLD/STATUS.md` are the authoritative running logs — **read the relevant
one before starting work**, and update it when a phase completes. They carry the full root-cause
chain, per-machine environment gotchas, and "how to resume after a restart" steps that are not
derivable from the code.

## Architecture

```
caller ──HTTP──> printgateway (HLD/deploy) ──`lp -d <queue>`──> cupsd (WSL) ──IPP──> ippfix ──IPP──> printer
                                                                     └──── or directly ──IPP──> printer
printersearch (Go IPP client) ────────raw IPP────────> a CUPS queue path, or the printer directly
```

- **`printersearch`** (root `main.go`+`ipp.go`; `HLD/WSL/` adds `bench.go`) — hand-rolled IPP
  client, stdlib only, no third-party IPP library. Subcommands `info` / `print` / `jobs` /
  `cancel`, plus `bench` in the HLD copy. It speaks IPP to *either* a printer's own endpoint or a
  CUPS queue path (`-path /printers/<queue>`) — that interchangeability is what the benchmarks rely on.
- **`ippfix`** — reverse proxy between CUPS and a printer whose firmware returns malformed IPP.
  The root version patches one bug (empty `naturalLanguage` values → `en-us`). `HLD/WSL/ippfix/` is
  the general version: it applies tag-level fixes to the **whole** message (the broken field appears
  in the response's operation-attributes group too, which strict clients check first) and then
  overlays the printer-attributes group against a captured golden template (`printer-template.json`)
  — live value wins if valid, template fills gaps. Generate a template with `-gen-template`.
- **`printgateway`** (`HLD/deploy/`) — HTTP prototype, `POST /print`, either `multipart/form-data`
  (file attached) or `application/json` `{"printer","file_url"}` (server downloads it). It does
  **not** speak IPP — it shells out to `lp -d <printer> <path>` and lets the pre-configured CUPS
  queue handle PPD/media/resolution/`ippfix`. See `HLD/deploy/README.md` for the full contract and
  the explicit list of HLD features deliberately not implemented. It is the only module wired to
  the shared **`github.com/LabOS-co/go-packages`** monorepo (`logs` for logging via
  `logs.GetConsoleLogger()`, `error_handler` for the standard labOS JSON error envelope) — see the
  README's "labOS shared library" section for why `GetConsoleLogger()`/env-var token rather than
  the full `settings`+`secret_store` stack, and follow that same pattern for any other real
  service extracted from this POC.
- **`win/`** and **`HLD/win-bench/`** — the control arm: print through the Windows spooler via
  `SumatraPDF -print-to`, to compare against the CUPS/IPP path. Not part of the deliverable.

## Build and run

```bash
# each module builds independently, from its own directory
go build -o printersearch.exe .                 # root
cd HLD/WSL && go build -o printersearch .       # HLD client + bench
cd HLD/deploy && GOOS=linux GOARCH=amd64 go build -o printgateway-linux-amd64 .
cd HLD/WSL/ippfix && GOOS=linux GOARCH=amd64 go build -o ippfix .
```

`printgateway` and `ippfix` must run where CUPS is (inside WSL); cross-compile from Windows and
copy the binary in, or build inside WSL.

```bash
# print through the working CUPS queue
./printersearch.exe print -host localhost -path /printers/brother-fixed -file printDemo.pdf -resolution 300
# inspect a printer's raw IPP capability response (first step in diagnosing any new printer)
./printersearch.exe info -host 192.168.252.210
# load test: N jobs at fixed concurrency across several CUPS queues
./printersearch bench -host 127.0.0.1 -port 631 -paths /printers/q-hp-laserjet,/printers/q-canon-ir \
  -requests 700 -concurrency 20 -file printDemo.pdf -wait-completion
# gateway
PRINT_GATEWAY_TOKEN='<secret>' ./printgateway-linux-amd64        # 127.0.0.1:8090 by default
```

WSL environment setup/reset scripts (run as root inside `wsl -d Ubuntu-24.04 -u root`):
`HLD/WSL/setup-15-printers.sh` (15 `ippeveprinter` virtual printers as systemd units),
`setup-15-queues.sh` (matching CUPS queues), `setup-emulators.sh` /
`setup-cups-queues-for-emulators.sh` (the earlier 3-printer fleet), and
`cups-resource-bench.sh` (bench run plus cupsd/filter CPU+RSS sampling).

### Documents

Every `.docx` in `HLD/` is generated — never edit the Word file, edit the `build_*.js` beside it
and regenerate:

```bash
cd HLD && npm install && node build_hld_phase1.js     # writes print-gateway-hld-phase1.docx
```

`build_spec.js` → spec, `build_spec_ha.js` → HA/queue/security spec, `build_hld_foundation.js` and
`build_hld_phase1.js` → HLD docs, `build_comparison.js` → CUPS-vs-spooler perf comparison; each has
an `_en` English twin. Each script writes exactly one file, named at the bottom of the script.

## Conventions and hard-won constraints

- **Go stdlib only** in `printersearch`/`ippfix`/`win`. No third-party dependency in any of those
  three modules; IPP encoding/decoding is written by hand against RFC 8010/8011. Keep it that way.
  `printgateway` is the one exception, since it depends on the shared labOS `go-packages` library
  (see above) — that's expected for anything that's an actual HTTP service rather than a CLI/proxy.
- **Comments explain *why*, at length.** Most non-obvious lines here exist because of a diagnosed
  bug (a printer firmware violation, a cups-filters bug, an IPP attribute-name mistake). When you
  work around something, write down the root cause next to it — that is the established style.
- **Hebrew RTL docx**: mixed Hebrew/English text must go through `splitBidiSegments()`/`Runs()`,
  which puts Latin spans in their own `TextRun` with `rightToLeft: false`. Do **not** insert Unicode
  bidi control characters (LRI U+2066 / PDI U+2069) — that was tried and rendered as visible
  "LRI"/"PDI" boxes in the user's Word. `build_spec.js` and `build_comparison*.js` still use the old
  unfixed helpers; the user explicitly declined to retrofit them — leave them alone unless asked.
- **Don't overwrite existing deliverables.** The user has repeatedly asked for new material as a new
  standalone document rather than merged into or replacing an existing one.
- **IPP attribute names are exact**: it is `printer-resolution`, not `print-resolution` — a wrong
  name is silently ignored and Ghostscript falls back to 600dpi/8-bit, overflowing the printer spool.
- **cups-filters version matters.** The original Debian bookworm setup pinned 1.28 deliberately;
  Ubuntu 24.04 + 2.0.0 is stricter and refused the raw malformed printer response entirely (which is
  exactly why the HLD `ippfix` had to be generalized). 1.28.17's generated PPD also needs its
  `cupsFilter2` line changed from `image/urf` to `image/pwg-raster` to dodge a raster margin bug;
  2.0.0 appears to have fixed that, but **that is verified only against the emulator, not the real printer.**
- **Prefer a static PPD queue over live `-m everywhere` negotiation** against a printer with a broken
  capability response — generate the PPD once via `driverless cat ipp://127.0.0.1:6310/ipp/print`
  pointed through `ippfix`.

## Environment gotchas (this machine)

- **Fixed (2026-09-01, Workstream G2):** `HLD/WSL/*.sh` and `HLD/win-bench/win-bench.ps1` used to
  hardcode `C:\printerSearch` / `/mnt/c/printerSearch` (a path that hasn't existed since the working
  copy became `C:\GitProjects\printer-server`, with a case typo — `HDL` vs `HLD` — that only worked
  because drvfs is case-insensitive). Each now derives its base path from its own location instead.
  Some in-repo *comments* (this file included, historically) may still cite the old path when
  describing something that predates the fix — that's prose, not something a script reads.
- Git Bash/MSYS mangles Unix-looking paths passed through `wsl.exe` or `docker exec`
  (`/mnt/c/...`, `/printers/brother-fixed`) — prefix such commands with `MSYS_NO_PATHCONV=1`.
- Long-running processes in WSL must be **systemd units**. `wsl.exe ... bash -c "... &"` does not
  keep background processes alive after the invoking command returns.
- CUPS in WSL must run as a plain always-on `cups.service`: `systemctl disable --now cups.socket cups.path`
  — socket/path activation plus `IdleExitTimeout` silently restarted cupsd and lost `lpadmin` queues.
- `MaxJobs` was raised from the 500 default to 2000 in `/etc/cups/cupsd.conf`; the 700-job load test
  hit real "Too many active jobs" rejections at 500.
- **VS Code's Go tab can go stale when a file is edited by an agent rather than through the editor.**
  If a `.go` file open in VS Code is overwritten by a tool/agent, the tab sometimes keeps its old
  in-memory buffer (shown by a filled dot instead of the close ✕) instead of auto-reloading from
  disk, and `gopls` then reports compiler errors against that stale buffer — e.g. "undefined"
  errors for symbols that were removed, or for methods that were added, none of which are real.
  Confirm with `go build ./...` from a shell first: if that's clean but the editor shows errors,
  the fix is `Ctrl+Shift+P` → **Revert File** on the affected tab (not Save — saving would write
  the stale buffer back over the correct on-disk content), then **Go: Restart Language Server**.
