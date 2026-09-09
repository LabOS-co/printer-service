#!/usr/bin/env bash
# Run the Postman collection against the currently-selected profile.
#
#   run-newman.sh                       # whatever profile.sh last selected
#   run-newman.sh A
#   run-newman.sh C --folder "s3_key intake" --folder Presign
#   run-newman.sh C --with-paper        # include the physically-printing folder
#   run-newman.sh A --bail              # stop at the first failure
#
# A scenario whose profile does not provide what it needs reports as SKIPPED,
# not as a failure — so the same collection is green under every profile and
# each profile simply exercises a different subset. That is what makes
# "run everything everywhere" cheap enough to actually do.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_cmd newman
load_vars

PROFILE=""
FOLDERS=()
EXTRA=()
PAPER=false

while (($#)); do
  case "$1" in
    --folder) FOLDERS+=(--folder "$2"); shift ;;
    --with-paper) PAPER=true ;;
    --bail) EXTRA+=(--bail) ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    -*) EXTRA+=("$1") ;;
    *) PROFILE="$1" ;;
  esac
  shift
done

[[ -n "$PROFILE" ]] || PROFILE="$(current_profile)"
[[ -n "$PROFILE" ]] || die "no profile selected — run: tests/scripts/profile.sh A"

running="$(current_profile)"
if [[ "$running" != "$PROFILE" ]]; then
  warn "the gateway is running profile '${running:-<none>}' but you asked for '$PROFILE'."
  warn "run: tests/scripts/profile.sh $PROFILE"
  die  "refusing to run — a mismatched profile produces confidently wrong results, not obvious errors"
fi

# Two cheap exec calls, run before every newman invocation rather than only in
# run-all.sh: the runbooks in TEST-PLAN.md section 19 call this script directly,
# and a dead queue backend turns every print row into a false green (see
# check_print_queue in lib.sh for the incident that motivated it).
check_print_queue "${GW_PRINTER:-vp1}"

COLLECTION="$TESTS_DIR/postman/printgateway.postman_collection.json"
ENVFILE="$TESTS_DIR/postman/env/${PROFILE}.postman_environment.json"

# Regenerate before every run, and refuse to run if generation fails.
#
# The collection is a generated artefact (see build-collection.js). Without
# this, a syntax error in the generator leaves the previous collection on disk
# and the suite keeps running happily against it — which happened during
# development: three profiles were "re-run" against a stale collection and
# reported the exact failures the edit had just fixed. A stale green is worse
# than a red.
if command -v node >/dev/null 2>&1; then
  (cd "$TESTS_DIR" && node postman/build-collection.js >/dev/null) \
    || die "postman/build-collection.js failed — refusing to run against a possibly stale collection"
else
  warn "node not available; running against the collection as it is on disk"
fi

[[ -f "$COLLECTION" ]] || die "collection missing — run: node tests/postman/build-collection.js"
[[ -f "$ENVFILE" ]] || die "no environment for profile $PROFILE — run: node tests/postman/build-collection.js"

backend="$(current_backend)"
mkdir -p "$REPORT_DIR"
stamp="$(date +%Y%m%d-%H%M%S)"
report="$REPORT_DIR/${PROFILE}-${backend}-${stamp}.json"

say "newman · profile $PROFILE · s3 backend $backend · paper=$PAPER"

# --working-dir is what makes the collection's relative formdata paths
# ("testdata/printDemo.pdf") resolve. Reading files outside that directory is
# allowed by default in newman 6 (--no-insecure-file-read would forbid it), so
# no flag is needed here — newman 5's explicit --insecure-file-read is now an
# unknown option and hard-errors.
set +e
newman run "$COLLECTION" \
  --environment "$ENVFILE" \
  --env-var "s3Backend=$backend" \
  --env-var "paper=$PAPER" \
  --working-dir "$TESTS_DIR" \
  --reporters cli,json \
  --reporter-json-export "$report" \
  --timeout-request 120000 \
  "${FOLDERS[@]}" "${EXTRA[@]}"
rc=$?
set -e

# Newman's CLI summary counts a skipped test as neither passed nor failed, and
# prints the total in a table that is easy to skim past. Restate it here so a
# run that skipped 60 scenarios because the profile was wrong cannot look like
# a clean pass.
if [[ -f "$report" ]]; then
  python3 - "$report" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
run = r.get("run", {})
stats = run.get("stats", {})
assertions = run.get("executions", [])
skipped, failed = [], []
for ex in assertions:
    for a in ex.get("assertions", []) or []:
        name = a.get("assertion", "")
        if a.get("skipped"):
            skipped.append("%s :: %s" % (ex["item"]["name"], name))
        elif a.get("error"):
            failed.append("%s :: %s — %s" % (ex["item"]["name"], name,
                                             a["error"].get("message", "")[:160]))
print()
print("  requests   %d" % stats.get("requests", {}).get("total", 0))
print("  assertions %d passed, %d failed, %d skipped" % (
    stats.get("assertions", {}).get("total", 0) - stats.get("assertions", {}).get("failed", 0) - len(skipped),
    stats.get("assertions", {}).get("failed", 0),
    len(skipped)))
if skipped:
    print("\n  skipped (profile does not provide what they need):")
    seen = set()
    for s in skipped:
        scen = s.split(" :: ")[0]
        if scen in seen: continue
        seen.add(scen)
        print("    - %s" % s.split(" :: ")[1].replace("SKIPPED — ", "").rjust(0) if False else "    - %s" % s)
if failed:
    print("\n  failed:")
    for f in failed:
        print("    ! %s" % f)
PY
  note "report: $report"
fi

exit $rc
