#!/usr/bin/env bash
# Open or UPDATE the one issue for a failing conformance checkpoint (AC-11).
#
#   scripts/conformance/file-issue.sh \
#     --tool claude_code --version 2.1.272 --os ubuntu-latest \
#     --chain A --step after-signal --checkpoint pointer \
#     --seed 4211 --run-url https://github.com/... [--summary FILE] [--dry-run]
#
# ⚠️ **DEDUPLICATION IS THE WHOLE POINT.** A step that files a new issue every
# run is worse than no step: the fifth duplicate is what makes people stop
# reading the first. The title IS the key —
#
#   conformance: <tool> <version> <os> <chain>/<step> <checkpoint>
#
# — and it is looked up with `gh issue list --search` on the EXACT string, then
# filtered locally for an exact title match, because GitHub's search is a text
# index: it tokenises, it is eventually consistent, and `--search "…"` alone
# will happily return a near-miss. A near-miss accepted as a hit would comment
# on the wrong issue; a near-miss rejected costs one duplicate. So the local
# equality check decides, and only `state:all` is searched — a reopened break
# belongs on its closed issue, not on a new one.
#
# --dry-run prints what it would do and touches nothing. The dedup path is
# exercised by pointing PATH at a fake `gh` (see docs/conformance.md).
set -uo pipefail

TOOL="" VERSION="" OS="" CHAIN="" STEP="" CHECKPOINT="" SEED=""
RUN_URL="" ARTIFACT_URL="" SUMMARY_FILE="" REPO="${GH_REPO:-}"
DRY=0

while [ $# -gt 0 ]; do
  case "$1" in
    --tool) TOOL=$2; shift 2 ;;
    --version) VERSION=$2; shift 2 ;;
    --os) OS=$2; shift 2 ;;
    --chain) CHAIN=$2; shift 2 ;;
    --step) STEP=$2; shift 2 ;;
    --checkpoint) CHECKPOINT=$2; shift 2 ;;
    --seed) SEED=$2; shift 2 ;;
    --run-url) RUN_URL=$2; shift 2 ;;
    --artifact-url) ARTIFACT_URL=$2; shift 2 ;;
    --summary) SUMMARY_FILE=$2; shift 2 ;;
    --repo) REPO=$2; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    -h|--help) sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "file-issue.sh: unknown flag $1" >&2; exit 2 ;;
  esac
done

# Plain and bash-3.2-safe (macOS ships 3.2, and this is run by hand there).
[ -n "$TOOL" ]       || { echo "file-issue.sh: --tool is required" >&2; exit 2; }
[ -n "$OS" ]         || { echo "file-issue.sh: --os is required" >&2; exit 2; }
[ -n "$CHAIN" ]      || { echo "file-issue.sh: --chain is required" >&2; exit 2; }
[ -n "$STEP" ]       || { echo "file-issue.sh: --step is required" >&2; exit 2; }
[ -n "$CHECKPOINT" ] || { echo "file-issue.sh: --checkpoint is required" >&2; exit 2; }
[ -n "$VERSION" ] || VERSION="unknown"

TITLE="conformance: $TOOL $VERSION $OS $CHAIN/$STEP $CHECKPOINT"

BODY_FILE=$(mktemp)
trap 'rm -f "$BODY_FILE"' EXIT
{
  echo "A conformance chain failed a checkpoint."
  echo
  echo "| field | value |"
  echo "| --- | --- |"
  echo "| tool | \`$TOOL\` |"
  echo "| tool version | \`$VERSION\` |"
  echo "| os | \`$OS\` |"
  echo "| chain / step | \`$CHAIN\` / \`$STEP\` |"
  echo "| checkpoint | \`$CHECKPOINT\` |"
  # The seed is what makes the failure reproducible: it decides the before/after
  # split, and `make conformance SEED=<seed>` replays exactly this run.
  echo "| seed | \`${SEED:-unknown}\` |"
  echo "| run | ${RUN_URL:-—} |"
  echo "| artifacts | ${ARTIFACT_URL:-in the run’s Artifacts section} |"
  echo
  if [ -n "$SUMMARY_FILE" ] && [ -f "$SUMMARY_FILE" ]; then
    echo "### Checkpoints"
    echo
    cat "$SUMMARY_FILE"
    echo
  fi
  echo "Reproduce locally:"
  echo
  echo '```'
  echo "make conformance TOOL=$TOOL CHAIN=$CHAIN SEED=${SEED:-1}      # this machine's tool"
  echo "make conformance-linux TOOL=$TOOL CHAIN=$CHAIN SEED=${SEED:-1} # tool@latest in the container"
  echo '```'
  echo
  echo "_Filed by \`scripts/conformance/file-issue.sh\`; the title is the dedup key, so a repeat of the same break updates this issue instead of opening another._"
} > "$BODY_FILE"

# ⚠️ The repo flag is expanded with ${REPO:+…} rather than held in an array:
# under `set -u`, "${arr[@]}" on an EMPTY array is an unbound-variable error in
# bash 3.2 (what macOS ships), which the fake-gh dedup test caught on its first
# run — which is what that test is for.

if [ "$DRY" = "1" ]; then
  echo "DRY RUN"
  echo "title: $TITLE"
  echo "--- body ---"
  cat "$BODY_FILE"
  exit 0
fi

command -v gh >/dev/null || { echo "file-issue.sh: gh is not on PATH" >&2; exit 2; }

# Exact-title match over open AND closed issues. `--search` is the index query;
# the title equality below is the decision.
export CONFORM_TITLE="$TITLE"
EXISTING=$(gh issue list ${REPO:+--repo "$REPO"} --state all --limit 50 \
             --search "\"$TITLE\" in:title" --json number,title,state 2>/dev/null \
           | python3 -c '
import json, os, sys
want = os.environ["CONFORM_TITLE"]
try:
    rows = json.load(sys.stdin)
except Exception:
    rows = []
for r in rows:
    if r.get("title") == want:
        print(r["number"], r["state"])
        break
')

if [ -n "$EXISTING" ]; then
  NUMBER=${EXISTING%% *}
  STATE=${EXISTING##* }
  echo "file-issue.sh: updating existing issue #$NUMBER ($STATE)"
  # A comment, not an edit of the body: the history of "it broke again, here"
  # is the useful part, and an edited body loses it.
  gh issue comment ${REPO:+--repo "$REPO"} "$NUMBER" --body-file "$BODY_FILE"
  if [ "$STATE" = "CLOSED" ]; then
    gh issue reopen ${REPO:+--repo "$REPO"} "$NUMBER" || true
  fi
  echo "issue=$NUMBER"
  echo "action=updated"
else
  echo "file-issue.sh: opening a new issue"
  # The label is best-effort: a repo without a `conformance` label must still
  # get the issue, so the unlabelled create is the fallback rather than a
  # failure.
  URL=$(gh issue create ${REPO:+--repo "$REPO"} --title "$TITLE" --body-file "$BODY_FILE" \
          --label conformance 2>/dev/null \
        || gh issue create ${REPO:+--repo "$REPO"} --title "$TITLE" --body-file "$BODY_FILE")
  echo "$URL"
  echo "action=created"
fi
