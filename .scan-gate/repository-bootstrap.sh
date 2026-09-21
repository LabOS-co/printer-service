#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: .scan-gate/repository-bootstrap.sh [--skip-hooks]

Run from the repository root. Safe to re-run. Does all of:
  - Install pre-commit + trufflehog if missing (user-local)
  - Set git init.templateDir (~/.labos-git-template) for later clones
  - Register hooks in THIS clone (Husky repos skip pre-commit install)

Options:
  --skip-hooks        Tools + git template only; do not call install-hooks in cwd
  --existing-clone    Ignored (this is already the default)

Example:
  bash .scan-gate/repository-bootstrap.sh
EOF
}

labos_scan_gate_default_git_template() {
  local exec_path share d
  exec_path="$(git --exec-path 2>/dev/null || true)"
  if [ -n "$exec_path" ]; then
    share="$(cd "$exec_path/../.." 2>/dev/null && pwd)/share/git-core/templates"
    if [ -d "$share/hooks" ]; then
      printf '%s\n' "$share"
      return 0
    fi
  fi
  for d in \
    /usr/share/git-core/templates \
    /usr/local/share/git-core/templates \
    /opt/homebrew/share/git-core/templates \
    /Library/Developer/CommandLineTools/usr/share/git-core/templates
  do
    if [ -d "$d/hooks" ]; then
      printf '%s\n' "$d"
      return 0
    fi
  done
  return 1
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE_SRC="${SCRIPT_DIR}/git-template"
TEMPLATE_DEST="${HOME}/.labos-git-template"
INSTALL_HOOKS=1

for arg in "$@"; do
  case "$arg" in
    --existing-clone) ;;
    --skip-hooks) INSTALL_HOOKS=0 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

bash "${SCRIPT_DIR}/ensure-tools.sh"

if [ ! -f "${TEMPLATE_SRC}/hooks/post-checkout" ]; then
  echo "ERROR: git template hook not found at ${TEMPLATE_SRC}/hooks/post-checkout" >&2
  exit 1
fi

rm -rf "$TEMPLATE_DEST"
mkdir -p "$TEMPLATE_DEST/hooks"

DEFAULT_TEMPLATE=""
if DEFAULT_TEMPLATE="$(labos_scan_gate_default_git_template)"; then
  echo "Seeding template from: $DEFAULT_TEMPLATE"
  cp -a "${DEFAULT_TEMPLATE}/." "$TEMPLATE_DEST/"
  mkdir -p "$TEMPLATE_DEST/hooks"
else
  echo "WARNING: system Git template not found; LabOS post-checkout only." >&2
fi

cp "${TEMPLATE_SRC}/hooks/post-checkout" "$TEMPLATE_DEST/hooks/post-checkout"
chmod +x "$TEMPLATE_DEST/hooks/post-checkout"

git config --global init.templateDir "$TEMPLATE_DEST"

echo ""
echo "Verification:"
echo "  init.templateDir: $(git config --global --get init.templateDir)"
echo "  pre-commit: $(pre-commit --version 2>/dev/null || echo 'not runnable')"
echo "  trufflehog: $(trufflehog --version 2>/dev/null | head -n 1 || echo 'not runnable')"
echo ""
echo "Later clones of repos with .scan-gate/ will auto-run install (Husky repos skip pre-commit install)."

if [ "$INSTALL_HOOKS" -eq 1 ]; then
  if [ -f ".scan-gate/install-hooks.sh" ]; then
    bash ".scan-gate/install-hooks.sh"
  else
    echo "No .scan-gate/install-hooks.sh in $(pwd) — git template is set; cd to a repo root to register this clone."
  fi
else
  echo "Skipped hook install (--skip-hooks). From a repo root: bash .scan-gate/repository-bootstrap.sh"
fi
