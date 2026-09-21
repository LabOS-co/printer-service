#!/usr/bin/env bash
set -euo pipefail

if ! command -v trufflehog >/dev/null 2>&1; then
  echo "trufflehog is required but was not found in PATH." >&2
  exit 127
fi

exclude_file="$(mktemp)"
trap 'rm -f "$exclude_file"' EXIT
printf '^\\.git/\n' > "$exclude_file"

trufflehog filesystem . \
  --config .trufflehog.yaml \
  --exclude-paths "$exclude_file" \
  --results=verified,unknown,unverified \
  --fail \
  --no-update \
  --force-skip-binaries
