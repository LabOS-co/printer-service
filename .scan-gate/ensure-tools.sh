#!/usr/bin/env bash
set -euo pipefail

labos_scan_gate_should_skip() {
  [ "${CI:-}" = "true" ] \
    || [ -n "${JENKINS_URL:-}" ] \
    || [ "${GITHUB_ACTIONS:-}" = "true" ] \
    || [ "${LABOS_SCAN_GATE_SKIP:-}" = "1" ]
}

labos_scan_gate_user_bins() {
  printf '%s\n' "${HOME}/.local/bin" "${HOME}/bin"
}

labos_scan_gate_prepend_path() {
  local dir added=""
  for dir in "$@"; do
    [ -n "$dir" ] || continue
    if [ -d "$dir" ] && [[ ":${PATH}:" != *":${dir}:"* ]]; then
      PATH="${dir}:${PATH}"
      added=1
    fi
  done
  if [ -n "$added" ]; then
    export PATH
  fi
}

labos_scan_gate_python_user_bin() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -m site --user-base 2>/dev/null | awk '{print $0 "/bin"}'
  elif command -v python >/dev/null 2>&1; then
    python -m site --user-base 2>/dev/null | awk '{print $0 "/bin"}'
  fi
}

labos_scan_gate_pip_install() {
  local py="$1"
  shift
  if "$py" -m pip install --user "$@"; then
    return 0
  fi
  echo "pip install --user failed; retrying with --break-system-packages (PEP 668)." >&2
  if "$py" -m pip install --user --break-system-packages "$@"; then
    return 0
  fi
  if command -v pipx >/dev/null 2>&1; then
    echo "Trying pipx..." >&2
    pipx install pre-commit
    return $?
  fi
  return 1
}

# Idempotent PATH snippet: .profile (bash/WSL), .zprofile (macOS zsh).
# Only append to .bash_profile when that file already exists (do not create it).
labos_scan_gate_persist_path() {
  local dest_home="${LABOS_SCAN_GATE_PROFILE_DIR:-$HOME}"
  local pybin marker snippet file
  mkdir -p "$dest_home" 2>/dev/null || true
  pybin="$(labos_scan_gate_python_user_bin)"
  marker="# LabOS scan-gate PATH"
  snippet='export PATH="$HOME/.local/bin:$HOME/bin:$PATH"'
  if [ -n "$pybin" ]; then
    snippet="export PATH=\"\$HOME/.local/bin:\$HOME/bin:${pybin}:\$PATH\""
  fi
  for file in "${dest_home}/.profile" "${dest_home}/.zprofile"; do
    if [ -f "$file" ] && grep -F -q "$marker" "$file" 2>/dev/null; then
      continue
    fi
    printf '\n%s\n%s\n' "$marker" "$snippet" >> "$file"
    echo "LABOS scan-gate: added PATH snippet to $file (restart Cursor/VS Code)."
  done
  file="${dest_home}/.bash_profile"
  if [ -f "$file" ] && ! grep -F -q "$marker" "$file" 2>/dev/null; then
    printf '\n%s\n%s\n' "$marker" "$snippet" >> "$file"
    echo "LABOS scan-gate: added PATH snippet to $file (restart Cursor/VS Code)."
  fi
}

if labos_scan_gate_should_skip; then
  echo "LABOS scan-gate: skipping tool auto-install (CI/automation)."
  exit 0
fi

user_bins="$(labos_scan_gate_user_bins)"
while IFS= read -r bin_dir; do
  [ -n "$bin_dir" ] || continue
  mkdir -p "$bin_dir"
done <<< "$user_bins"

labos_scan_gate_prepend_path \
  "${HOME}/.local/bin" \
  "${HOME}/bin" \
  "$(labos_scan_gate_python_user_bin)"

if command -v pre-commit >/dev/null 2>&1 && command -v trufflehog >/dev/null 2>&1; then
  labos_scan_gate_persist_path
  echo "pre-commit: $(pre-commit --version)"
  echo "trufflehog: $(trufflehog --version 2>&1 | head -n 1)"
  exit 0
fi

if ! command -v pre-commit >/dev/null 2>&1; then
  if command -v python3 >/dev/null 2>&1; then
    labos_scan_gate_pip_install python3 pre-commit
  elif command -v python >/dev/null 2>&1; then
    labos_scan_gate_pip_install python pre-commit
  else
    echo "ERROR: python3/python not found; cannot install pre-commit." >&2
    echo "Install manually: python3 -m pip install --user pre-commit" >&2
    exit 1
  fi
fi

labos_scan_gate_prepend_path \
  "${HOME}/.local/bin" \
  "${HOME}/bin" \
  "$(labos_scan_gate_python_user_bin)"

if ! command -v trufflehog >/dev/null 2>&1; then
  install_dir="${HOME}/.local/bin"
  mkdir -p "$install_dir"
  if ! curl -sSfL https://raw.githubusercontent.com/trufflesecurity/trufflehog/main/scripts/install.sh \
    | sh -s -- -b "$install_dir"; then
    echo "ERROR: trufflehog install script failed." >&2
    echo "Install manually: https://github.com/trufflesecurity/trufflehog/releases" >&2
    exit 1
  fi
  labos_scan_gate_prepend_path "$install_dir" "${HOME}/bin"
fi

missing=0
if ! command -v pre-commit >/dev/null 2>&1; then
  echo "ERROR: pre-commit is still not on PATH after install." >&2
  echo 'Add to ~/.profile / ~/.zprofile: export PATH="$HOME/.local/bin:$HOME/bin:$PATH"' >&2
  echo "WSL/Cursor: if the IDE still cannot see the tools, symlink into /usr/local/bin (see README)." >&2
  missing=1
fi
if ! command -v trufflehog >/dev/null 2>&1; then
  echo "ERROR: trufflehog is still not on PATH after install." >&2
  echo 'Add to ~/.profile / ~/.zprofile: export PATH="$HOME/.local/bin:$HOME/bin:$PATH"' >&2
  missing=1
fi

if [ "$missing" -ne 0 ]; then
  exit 1
fi

labos_scan_gate_persist_path
echo "pre-commit: $(pre-commit --version)"
echo "trufflehog: $(trufflehog --version 2>&1 | head -n 1)"
