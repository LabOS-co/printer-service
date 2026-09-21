#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$REPO_ROOT"

labos_scan_gate_should_skip() {
  [ "${CI:-}" = "true" ] \
    || [ -n "${JENKINS_URL:-}" ] \
    || [ "${GITHUB_ACTIONS:-}" = "true" ] \
    || [ "${LABOS_SCAN_GATE_SKIP:-}" = "1" ]
}

ensure_tools="${REPO_ROOT}/.scan-gate/ensure-tools.sh"
if [ ! -f "$ensure_tools" ] && [ -f "${SCRIPT_DIR}/ensure-tools.sh" ]; then
  ensure_tools="${SCRIPT_DIR}/ensure-tools.sh"
fi

if [ -f "$ensure_tools" ] && ! labos_scan_gate_should_skip; then
  bash "$ensure_tools"
elif [ ! -f "$ensure_tools" ] && ! labos_scan_gate_should_skip; then
  echo "ensure-tools.sh not found; checking PATH only." >&2
fi

missing=0
if ! command -v pre-commit >/dev/null 2>&1; then
  echo "pre-commit is not installed." >&2
  echo "Run .scan-gate/repository-bootstrap.sh or: python3 -m pip install --user pre-commit" >&2
  missing=1
fi
if ! command -v trufflehog >/dev/null 2>&1; then
  echo "trufflehog is not installed." >&2
  echo "Run .scan-gate/repository-bootstrap.sh or install from https://github.com/trufflesecurity/trufflehog/releases" >&2
  missing=1
fi
if [ "$missing" -ne 0 ]; then
  exit 127
fi

uses_husky=0
if [ -d ".husky" ]; then
  uses_husky=1
elif [ -f ".git/hooks/pre-commit" ] && grep -qi husky ".git/hooks/pre-commit" 2>/dev/null; then
  uses_husky=1
fi

if [ "$uses_husky" -eq 1 ]; then
  echo "This repository uses Husky for Git hooks."
  echo "Do not run 'pre-commit install' here - wire scan-gate from .husky/* (for LaaS: pnpm install)."
  exit 0
fi

if [ ! -f ".pre-commit-config.yaml" ] && [ -f ".pre-commit-config.windows.yaml" ]; then
  cp ".pre-commit-config.windows.yaml" ".pre-commit-config.yaml"
  echo "Copied .pre-commit-config.windows.yaml to .pre-commit-config.yaml"
fi

pre-commit install --hook-type pre-commit --hook-type pre-push
pre-commit install-hooks

echo "Scan gate installed for pre-commit and pre-push."
