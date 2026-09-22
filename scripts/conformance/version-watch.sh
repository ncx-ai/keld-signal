#!/usr/bin/env bash
# The tool-release canary: which supported tool published a version we have not
# proven yet.
#
#   scripts/conformance/version-watch.sh                 # report
#   scripts/conformance/version-watch.sh --matrix        # print the matrix JSON
#   scripts/conformance/version-watch.sh --record claude_code 2.1.272
#
# This is what REPLACES a nightly full matrix. A nightly run with nothing new to
# test proves nothing new and spends the money in the wrong place (spec §2), so
# the daily job is one minute of `npm view` and a run happens only on a real
# change.
#
# Output on --matrix is `{"include":[{"tool":…,"version":…,"os":"ubuntu-latest",
# "chain":"A"}]}` — empty include when nothing moved, which the workflow treats
# as "no job", not as a failure.
set -uo pipefail

FILE=${KELD_CONFORM_STATE:-.conformance/last-tested.json}
MODE=report
RECORD_TOOL=""
RECORD_VERSION=""

while [ $# -gt 0 ]; do
  case "$1" in
    --file)   FILE=$2; shift 2 ;;
    --matrix) MODE=matrix; shift ;;
    --record) MODE=record; RECORD_TOOL=$2; RECORD_VERSION=$3; shift 3 ;;
    -h|--help) sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "version-watch.sh: unknown flag $1" >&2; exit 2 ;;
  esac
done

[ -f "$FILE" ] || { echo "version-watch.sh: no state file at $FILE" >&2; exit 2; }
command -v python3 >/dev/null || { echo "version-watch.sh: python3 is required" >&2; exit 2; }

# --record <tool> <version>: write back a PROVEN version. Separated from the
# read path so the only way a version lands in the file is a run that passed.
if [ "$MODE" = record ]; then
  python3 - "$FILE" "$RECORD_TOOL" "$RECORD_VERSION" <<'PY'
import json, sys, datetime
path, tool, version = sys.argv[1], sys.argv[2], sys.argv[3]
doc = json.load(open(path))
entry = doc["tools"].get(tool)
if entry is None:
    sys.exit(f"version-watch.sh: {tool} is not in {path}")
entry["version"] = version
entry["tested_on"] = datetime.date.today().isoformat()
entry["proven_by"] = "conformance.yml, chain A on ubuntu-latest"
json.dump(doc, open(path, "w"), indent=2)
open(path, "a").write("\n")
print(f"recorded {tool} {version}")
PY
  exit $?
fi

# Read the published version per tool. `npm view` is one network call each and
# the whole job is ~1 minute, which is the point.
published() {
  npm view "$1" version 2>/dev/null | tail -1
}

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

# ⚠️ The field separator is `|`, not a tab. Tab is IFS *whitespace*, so bash
# collapses consecutive tabs and an empty `version` silently shifted `harness`
# into it — every unproven tool then read as harness "" and was reported under
# the wrong column.
# ⚠️ WHETHER THE HARNESS CAN RUN A TOOL IS ASKED OF THE HARNESS, not of this
# JSON. It used to be a `harness` key here, and the two drifted immediately:
# `lib/tools.sh` ran codex while this file called it "pending", so the canary
# reported every Codex release as "not in the harness yet" and dispatched
# nothing. One fact, one owner — `tool_supported` in lib/tools.sh.
#
# What stays in the JSON is only what has been PROVEN: the version, when, and
# by which run. A record of evidence, not a second configuration.
. "$(dirname "$0")/lib/tools.sh"

python3 - "$FILE" > "$TMP" <<'PY'
import json, sys
doc = json.load(open(sys.argv[1]))
for tool, e in doc["tools"].items():
    print("|".join([tool, e["package"], str(e.get("version") or "")]))
PY

CHANGED=""
REPORT=""
while IFS='|' read -r tool pkg known; do
  [ -n "$tool" ] || continue
  latest=$(published "$pkg")
  if [ -z "$latest" ]; then
    REPORT="$REPORT$tool\t$pkg\t${known:-—}\tunreachable\tskipped (npm view gave nothing)\n"
    continue
  fi
  if ! tool_supported "$tool"; then
    REPORT="$REPORT$tool\t$pkg\t${known:-—}\t$latest\tnot in the harness yet (never dispatched)\n"
    continue
  fi
  if [ -z "$known" ]; then
    # First sight RECORDS rather than dispatching: a version nobody has proven
    # is not a change, and dispatching every unknown tool on day one is the
    # herd the watcher's own first-sight rule exists to avoid.
    REPORT="$REPORT$tool\t$pkg\t—\t$latest\tfirst sight (record, no run)\n"
    continue
  fi
  if [ "$known" = "$latest" ]; then
    REPORT="$REPORT$tool\t$pkg\t$known\t$latest\tunchanged\n"
  else
    REPORT="$REPORT$tool\t$pkg\t$known\t$latest\tCHANGED -> chain A on ubuntu-latest\n"
    CHANGED="$CHANGED$tool:$latest "
  fi
done < "$TMP"

if [ "$MODE" = matrix ]; then
  python3 - "$CHANGED" <<'PY'
import json, sys
include = []
for item in sys.argv[1].split():
    tool, _, version = item.partition(":")
    include.append({"tool": tool, "version": version, "os": "ubuntu-latest", "chain": "A"})
print(json.dumps({"include": include}))
PY
  exit 0
fi

printf 'tool\tpackage\tproven\tpublished\taction\n'
printf '%b' "$REPORT"
[ -n "$CHANGED" ] && echo "changed: $CHANGED"
exit 0
