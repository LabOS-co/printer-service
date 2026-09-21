#!/usr/bin/env bash
set -euo pipefail

# Pre-push secret scan.
# - pre-commit framework: PRE_COMMIT_FROM_REF
# - Git/Husky: stdin lines "local_ref local_sha remote_ref remote_sha"
# Never fall back to the repo root commit (too slow/noisy).

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$ROOT"
export PATH="${HOME}/.local/bin:${HOME}/bin:${PATH}"

if ! command -v trufflehog >/dev/null 2>&1; then
  if [ -f "${ROOT}/.scan-gate/ensure-tools.sh" ]; then
    bash "${ROOT}/.scan-gate/ensure-tools.sh" || true
    export PATH="${HOME}/.local/bin:${HOME}/bin:${PATH}"
  fi
fi

if ! command -v trufflehog >/dev/null 2>&1; then
  echo "trufflehog is not on PATH." >&2
  echo "Run .scan-gate/ensure-tools.sh or .scan-gate/repository-bootstrap.sh" >&2
  exit 127
fi

zero_ref="0000000000000000000000000000000000000000"

scan_since() {
  local base="$1"
  echo "Running TruffleHog git scan since ${base}..."
  trufflehog git file://. \
    --config .trufflehog.yaml \
    --since-commit "$base" \
    --results=verified,unknown,unverified \
    --fail \
    --no-update
}

from_ref="${PRE_COMMIT_FROM_REF:-}"
if [ -n "$from_ref" ] && [ "$from_ref" != "$zero_ref" ] && git cat-file -e "$from_ref^{commit}" 2>/dev/null; then
  scan_since "$from_ref"
  exit 0
fi

scanned=0
if [ ! -t 0 ]; then
  while read -r local_ref local_sha remote_ref remote_sha; do
    [ -n "${local_sha:-}" ] || continue
    if [ "$local_sha" = "$zero_ref" ]; then
      continue
    fi
    base="$remote_sha"
    if [ -z "$base" ] || [ "$base" = "$zero_ref" ]; then
      if git rev-parse --verify origin/dev >/dev/null 2>&1; then
        base="$(git merge-base "$local_sha" origin/dev 2>/dev/null || true)"
      fi
      if [ -z "$base" ] && git rev-parse --verify origin/HEAD >/dev/null 2>&1; then
        base="$(git merge-base "$local_sha" origin/HEAD 2>/dev/null || true)"
      fi
      if [ -z "$base" ]; then
        echo "New branch ${local_ref:-unknown}: no merge-base with origin/dev; skipping history-wide scan." >&2
        continue
      fi
    fi
    if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
      echo "Skipping push range: base commit ${base} is not in this clone." >&2
      continue
    fi
    scan_since "$base"
    scanned=1
  done
fi

if [ "$scanned" -eq 0 ] && [ -t 0 ]; then
  if git rev-parse --abbrev-ref '@{upstream}' >/dev/null 2>&1; then
    scan_since "$(git rev-parse '@{upstream}')"
    scanned=1
  else
    echo "No push range and no upstream; skipping TruffleHog pre-push scan."
    exit 0
  fi
fi

exit 0
