#!/usr/bin/env bash
# Generate tests/testdata/ (TEST-PLAN.md section 5.4). Idempotent.
#
# printDemo.pdf is copied from the repo root rather than symlinked: Newman
# resolves formdata file paths against its --working-dir, and a symlink into
# /mnt/c behaves differently depending on which side created it — a copy has
# one behaviour everywhere.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

TD="$TESTS_DIR/testdata"
mkdir -p "$TD"

SRC_PDF=""
for c in "$REPO_DIR/printDemo.pdf" "$REPO_DIR/src/printersearch/testdata/printDemo.pdf"; do
  [[ -f "$c" ]] && { SRC_PDF="$c"; break; }
done
[[ -n "$SRC_PDF" ]] || die "printDemo.pdf not found in the repo"

# --- the standard document (2 pages, ~2.5 MB) -------------------------------
if [[ ! -f "$TD/printDemo.pdf" ]]; then
  cp "$SRC_PDF" "$TD/printDemo.pdf"
  note "printDemo.pdf   $(stat -c%s "$TD/printDemo.pdf") bytes (from $SRC_PDF)"
fi

# --- a genuinely small, genuinely valid single-page PDF ---------------------
# Hand-built rather than derived from printDemo.pdf: it must be under profile
# G's 1 MiB upload cap AND under its 64 KiB fetch/S3 caps, and it must really
# print (GW-MP-09 asserts a 200, which means CUPS accepted it). A truncated
# copy of a real PDF would satisfy the size constraint and fail the other.
if [[ ! -f "$TD/small.pdf" ]]; then
  python3 - "$TD/small.pdf" <<'PY'
import sys, zlib

# Minimal but structurally complete PDF 1.4: catalog, pages, one page, one
# content stream, one base-14 font. Offsets in the xref table are computed
# after assembly, so this stays valid if the objects are ever edited.
objs = []
objs.append(b"<< /Type /Catalog /Pages 2 0 R >>")
objs.append(b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
objs.append(b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] "
            b"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>")
stream = (b"BT /F1 18 Tf 72 760 Td (LAB-16894 print gateway test fixture) Tj ET\n"
          b"BT /F1 11 Tf 72 730 Td (small.pdf - one page, under every profile-G size limit) Tj ET\n")
objs.append(b"<< /Length %d >>\nstream\n" % len(stream) + stream + b"endstream")
objs.append(b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

out = bytearray(b"%PDF-1.4\n")
offsets = []
for i, body in enumerate(objs, start=1):
    offsets.append(len(out))
    out += b"%d 0 obj\n" % i + body + b"\nendobj\n"

xref = len(out)
out += b"xref\n0 %d\n" % (len(objs) + 1)
out += b"0000000000 65535 f \n"
for off in offsets:
    out += b"%010d 00000 n \n" % off
out += b"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, xref)

open(sys.argv[1], "wb").write(bytes(out))
PY
  note "small.pdf       $(stat -c%s "$TD/small.pdf") bytes (generated, valid 1-page PDF)"
fi

# --- documented-gap fixtures ------------------------------------------------
# Both assert that the service does NOT validate content (README: "No content
# validation of a fetched or uploaded document"). They are expected to print.
[[ -f "$TD/not-a-pdf.txt" ]] || {
  printf 'hello — this is not a PDF, and the gateway does not check.\n' > "$TD/not-a-pdf.txt"
  note "not-a-pdf.txt   $(stat -c%s "$TD/not-a-pdf.txt") bytes (documented gap: GW-MP-10 / GW-S3-10)"
}
[[ -f "$TD/empty.pdf" ]] || {
  : > "$TD/empty.pdf"
  note "empty.pdf       0 bytes (documented gap: GW-MP-11 / GW-S3-11)"
}

# --- oversize object for the S3 limit scenario ------------------------------
if [[ ! -f "$TD/large.bin" ]]; then
  head -c 131072 /dev/zero > "$TD/large.bin"   # 128 KiB, twice profile G's 64 KiB S3 cap
  note "large.bin       $(stat -c%s "$TD/large.bin") bytes (GW-S3-07)"
fi

# --- hostile filename -------------------------------------------------------
# The spool filename is built from the caller-supplied multipart part name.
# The literal traversal string cannot be a real filename on disk, so the file
# is ordinary here and the hostile name is applied by the request itself; this
# copy exists so the request has something to attach.
[[ -f "$TD/weird-name.pdf" ]] || {
  cp "$TD/small.pdf" "$TD/weird-name.pdf"
  note "weird-name.pdf  $(stat -c%s "$TD/weird-name.pdf") bytes (GW-MP-12)"
}

say "fixtures ready in $TD"
ls -l "$TD" | tail -n +2 | awk '{printf "    %-18s %8s bytes\n", $9, $5}'
