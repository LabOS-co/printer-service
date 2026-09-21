#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "Usage: .scan-gate/scan-git-range.sh <since-commit-or-ref>" >&2
  exit 2
fi

if ! command -v trufflehog >/dev/null 2>&1; then
  echo "trufflehog is required but was not found in PATH." >&2
  exit 127
fi

trufflehog git file://. \
  --config .trufflehog.yaml \
  --since-commit "$1" \
  --results=verified,unknown,unverified \
  --fail \
  --no-update
