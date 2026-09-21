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

trufflehog filesystem \
  --config .trufflehog.yaml \
  --results=verified,unknown,unverified \
  --fail \
  --no-update \
  --force-skip-binaries \
  "$@"
