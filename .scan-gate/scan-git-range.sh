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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=trufflehog-excludes.sh
. "$SCRIPT_DIR/trufflehog-excludes.sh"

# shellcheck disable=SC2046
trufflehog_run git file://. \
  --config .trufflehog.yaml \
  $(trufflehog_exclude_cli_args) \
  --since-commit "$1" \
  --results=verified,unknown,unverified \
  --no-update
