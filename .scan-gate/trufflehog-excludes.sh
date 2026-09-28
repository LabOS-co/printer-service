# Exclusions TruffleHog's own config cannot express for built-in detectors.
# exclude_words in .trufflehog.yaml applies only to custom detectors.
#
# .trufflehog-exclude-words — substring of the secret (Raw / RawV2). The file
# is still scanned; only matching findings are dropped.
# .trufflehog-exclude-paths — one regex per line. Skips the whole path.
# Blank lines and # comments are ignored in both files.

TRUFFLEHOG_FILTER_SH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/filter-findings.sh"

# Run trufflehog, then drop findings listed in .trufflehog-exclude-words.
# Exit 183 when any finding remains. Other non-zero codes are trufflehog's.
trufflehog_run() {
  local json err code filter_code
  json="$(mktemp)"
  err="$(mktemp)"
  set +e
  trufflehog "$@" --json >"$json" 2>"$err"
  code=$?
  set -e
  if [ -s "$err" ]; then
    cat "$err" >&2
  fi
  if [ "$code" -ne 0 ]; then
    rm -f "$json" "$err"
    return "$code"
  fi
  if [ ! -f "$TRUFFLEHOG_FILTER_SH" ]; then
    if grep -q '"DetectorName"' "$json"; then
      echo "TruffleHog found secrets. .scan-gate/filter-findings.sh is missing." >&2
      rm -f "$json" "$err"
      return 183
    fi
    rm -f "$json" "$err"
    return 0
  fi
  set +e
  bash "$TRUFFLEHOG_FILTER_SH" <"$json"
  filter_code=$?
  set -e
  rm -f "$json" "$err"
  return "$filter_code"
}

trufflehog_exclude_cli_args() {
  if [ -f .trufflehog-exclude-paths ]; then
    printf '%s\n' --exclude-paths .trufflehog-exclude-paths
  fi
}

trufflehog_filter_paths() {
  local f line skip normalized
  if [ ! -f .trufflehog-exclude-paths ]; then
    for f in "$@"; do
      printf '%s\0' "$f"
    done
    return 0
  fi
  for f in "$@"; do
    skip=0
    normalized=$(printf '%s' "$f" | tr '\\' '/')
    while IFS= read -r line || [ -n "$line" ]; do
      line=${line%$'\r'}
      case "$line" in
        ''|\#*) continue ;;
      esac
      if printf '%s\n' "$normalized" | grep -Eq -- "$line"; then
        skip=1
        break
      fi
    done < .trufflehog-exclude-paths
    if [ "$skip" -eq 0 ]; then
      printf '%s\0' "$f"
    else
      echo "TruffleHog: path excluded by .trufflehog-exclude-paths: $f" >&2
    fi
  done
}
