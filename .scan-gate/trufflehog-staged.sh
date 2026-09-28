#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -eq 0 ]; then
  exit 0
fi

if ! command -v trufflehog >/dev/null 2>&1; then
  echo "trufflehog is required but was not found in PATH." >&2
  echo "Install it from https://docs.trufflesecurity.com/pre-commit-hooks and retry." >&2
  exit 127
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=trufflehog-excludes.sh
. "$SCRIPT_DIR/trufflehog-excludes.sh"

filtered=()
while IFS= read -r -d '' f; do
  filtered+=("$f")
done < <(trufflehog_filter_paths "$@")

if [ "${#filtered[@]}" -eq 0 ]; then
  exit 0
fi

# shellcheck disable=SC2046
trufflehog_run filesystem \
  --config .trufflehog.yaml \
  $(trufflehog_exclude_cli_args) \
  --results=verified,unknown,unverified \
  --no-update \
  --force-skip-binaries \
  "${filtered[@]}"
