# printer-server

POC/prototype work for **LAB-16894** — a centralized print service that prints PDFs to network
printers via **CUPS + IPP**, bypassing the Windows print spooler and SumatraPDF. This is not a
product: it is a proven end-to-end path (real physical prints), a benchmark rig, Hebrew/English
spec + HLD documents, and a first Print Gateway HTTP prototype.

Three Go modules plus one Node module, no shared workspace — each has its own `go.mod`/`package.json`
and is built independently. There is no test suite and no linter config.

See [`CLAUDE.md`](CLAUDE.md) for the full architecture, build/run commands, and hard-won
constraints. `STATUS.md` (root) and [`docs/STATUS.md`](docs/STATUS.md) are the authoritative running
logs — read the relevant one before starting work.

## The two generations

- **Root** (`main.go`, `ipp.go`, `ippfix/`, `docker/`, `win/`) — the original Docker+CUPS POC.
  Frozen as a backup; not modified going forward.
- **`src/`** — all current work. CUPS runs natively in a WSL2 `Ubuntu-24.04` distro, `ippfix` was
  rewritten as a template-overlay proxy, and the benchmark/load-test rig and the Print Gateway
  prototype live here.

## Architecture

```
caller ──HTTP──> printgateway (src/printgateway) ──`lp -d <queue>`──> cupsd (WSL) ──IPP──> ippfix ──IPP──> printer
                                                                          └──── or directly ──IPP──> printer
printersearch (Go IPP client) ────────raw IPP────────> a CUPS queue path, or the printer directly
```

- **`printersearch`** — hand-rolled IPP client (stdlib only). `info` / `print` / `jobs` / `cancel`,
  plus `bench` in the `src/printersearch` copy.
- **`ippfix`** — reverse proxy that fixes malformed IPP responses from printer firmware before CUPS
  sees them. See [`src/printgateway/README.md`](src/printgateway/README.md) and `CLAUDE.md` for the
  two generations of this proxy.
- **`printgateway`** (`src/printgateway/`) — HTTP prototype (`POST /print`) that shells out to
  `lp -d <printer>`. Full contract in [`src/printgateway/README.md`](src/printgateway/README.md).
- **`win/`** / **`src/win-bench/`** — control arm: print through the Windows spooler via
  SumatraPDF, for comparison. Not part of the deliverable.

## Build

```bash
# each module builds independently; build from its module root (where go.mod
# lives) naming the ./cmd/<name> package, so the binary lands beside go.mod
# rather than nested under cmd/ - that's the path install-services.sh's
# DEPLOY/IPPFIX vars and .gitignore's binary entries both assume.
go build -o printersearch.exe .                           # root
cd src/printersearch && go build -o printersearch ./cmd/printersearch
cd src/printgateway && GOOS=linux GOARCH=amd64 go build -o printgateway-linux-amd64 ./cmd/printgateway
cd src/ippfix && GOOS=linux GOARCH=amd64 go build -o ippfix ./cmd/ippfix
```

`printgateway` and `ippfix` must run where CUPS is (inside WSL) — cross-compile from Windows and
copy the binary in, or build inside WSL.

## Documents

Every `.docx` under `docs/` is generated — edit the `build_*.js` script beside it and regenerate,
never edit the Word file directly:

```bash
cd docs && npm install && node build_hld_phase1.js     # writes print-gateway-hld-phase1.docx
```

## Status

Internal prototype for LabOS. Not a public project — no license file is included and this repo is
intended to stay private.
