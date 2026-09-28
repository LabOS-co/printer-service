#!/usr/bin/env bash
# Drop TruffleHog JSON findings whose secret contains a listed substring.
# Reads NDJSON on stdin. .trufflehog-exclude-words is one substring per line
# (blank lines and # comments ignored), matched against Raw and RawV2 only.
#
# Stdout stays empty unless TRUFFLEHOG_FILTER_JSON=1 (kept findings, for Jenkins).
# A short report goes to stderr. Exit 183 when any finding remains, else 0.

set -euo pipefail

json_unescape() {
  local s="$1" out="" hex
  while [ -n "$s" ]; do
    case "$s" in
      \\n*) out+=$'\n'; s="${s:2}" ;;
      \\r*) out+=$'\r'; s="${s:2}" ;;
      \\t*) out+=$'\t'; s="${s:2}" ;;
      \\u[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]*)
        hex="${s:2:4}"
        out+=$(printf '%b' "\\u${hex}")
        s="${s:6}"
        ;;
      \\*) out+="${s:1:1}"; s="${s:2}" ;;
      *) out+="${s:0:1}"; s="${s:1}" ;;
    esac
  done
  printf '%s' "$out"
}

json_string_field() {
  local json="$1" key="$2" re
  re="[,{]\"${key}\":\"((\\\\.|[^\"\\\\])*)\""
  if [[ "$json" =~ $re ]]; then
    json_unescape "${BASH_REMATCH[1]}"
    return 0
  fi
  return 1
}

json_number_field() {
  local json="$1" key="$2" re
  re="[,{]\"${key}\":([0-9]+)"
  if [[ "$json" =~ $re ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
    return 0
  fi
  return 1
}

WORDS=()
if [ -f .trufflehog-exclude-words ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    case "$line" in
      ''|\#*) continue ;;
    esac
    WORDS+=("$line")
  done < .trufflehog-exclude-words
fi

kept=0
ignored=0
emit_json=0
if [ "${TRUFFLEHOG_FILTER_JSON:-}" = "1" ]; then
  emit_json=1
fi

while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in
    \{*) ;;
    *) continue ;;
  esac

  detector=""
  raw=""
  raw_v2=""
  redacted=""
  path=""
  lineno=""
  detector="$(json_string_field "$line" DetectorName || true)"
  raw="$(json_string_field "$line" Raw || true)"
  raw_v2="$(json_string_field "$line" RawV2 || true)"
  if [ -z "$detector" ] && [ -z "$raw" ] && [ -z "$raw_v2" ]; then
    kept=$((kept + 1))
    echo "TruffleHog: unparsed finding, blocking" >&2
    continue
  fi

  blob="${raw}"$'\n'"${raw_v2}"
  drop=0
  if [ "${#WORDS[@]}" -gt 0 ]; then
    for word in "${WORDS[@]}"; do
      case "$blob" in
        *"$word"*) drop=1; break ;;
      esac
    done
  fi
  if [ "$drop" -eq 1 ]; then
    ignored=$((ignored + 1))
    continue
  fi

  kept=$((kept + 1))
  if [ "$emit_json" -eq 1 ]; then
    printf '%s\n' "$line"
  fi
  redacted="$(json_string_field "$line" Redacted || true)"
  path="$(json_string_field "$line" file || true)"
  lineno="$(json_number_field "$line" line || true)"
  echo "Detector: ${detector:-?}" >&2
  echo "File: ${path:-?}" >&2
  echo "Line: ${lineno:-?}" >&2
  if [ -n "$redacted" ]; then
    echo "Redacted: $redacted" >&2
  fi
  echo >&2
done

if [ "$ignored" -gt 0 ]; then
  echo "TruffleHog: ignored ${ignored} finding(s) matching .trufflehog-exclude-words" >&2
fi

if [ "$kept" -gt 0 ]; then
  exit 183
fi
exit 0
