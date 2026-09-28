#!/usr/bin/env bash
set -euo pipefail

if ! command -v trufflehog >/dev/null 2>&1; then
  echo "trufflehog is required but was not found in PATH." >&2
  exit 127
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=trufflehog-excludes.sh
. "$SCRIPT_DIR/trufflehog-excludes.sh"

exclude_file="$(mktemp)"
trap 'rm -f "$exclude_file"' EXIT
printf '^\\.git/\n' > "$exclude_file"
if [ -f .trufflehog-exclude-paths ]; then
  cat .trufflehog-exclude-paths >> "$exclude_file"
  printf '\n' >> "$exclude_file"
fi

trufflehog_run filesystem . \
  --config .trufflehog.yaml \
  --exclude-paths "$exclude_file" \
  --results=verified,unknown,unverified \
  --no-update \
  --force-skip-binaries
