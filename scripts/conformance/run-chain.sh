#!/usr/bin/env bash
# Run one conformance chain against a real tool, a mock model and a mock Atlas.
#
#   scripts/conformance/run-chain.sh --tool claude_code [--chain A] [--seed N] [--work DIR]
#
# What it proves (AC-10): the packaged onboarding commands configure a REAL tool
# that is already installed, and the five lanes carry that tool's next prompt all
# the way to Atlas — transcript written, pointer received, store rows for the
# session, telemetry forwarded, enrichment or block published.
#
# What it never does: reach the network, read the developer's own ~/.keld or
# ~/.claude, register a service, or keep any transcript but the one our own
# scripted prompt produced.
#
# Chain A is "a fresh install of the release under test":
#
#   the tool, installed and USED   ->  Signal onboarded  ->  one prompt  ->  5 checkpoints
#
# The before-half prompt is not ceremony. `tools.Detect` reports Claude Code
# installed by the existence of ~/.claude, so a tool that has never run is not
# configured by `keld signal setup` — and that prompt is also what makes the
# after-half session NEWER than the config, which is the `restart_required` row
# of the decision table.
#
# Tasks A.4/A.5 add the container and VM legs (where `install.sh` and the pkg
# run for real, AC-12) and the two-tool split chain A actually describes. This
# script is the single-tool local host case, deliberately.
set -uo pipefail

TOOL=""
CHAIN="A"
SEED=""
WORK=""

while [ $# -gt 0 ]; do
  case "$1" in
    --tool)  TOOL=$2; shift 2 ;;
    --chain) CHAIN=$2; shift 2 ;;
    --seed)  SEED=$2; shift 2 ;;
    --work)  WORK=$2; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "run-chain.sh: unknown flag $1" >&2; exit 2 ;;
  esac
done

[ -n "$TOOL" ] || { echo "run-chain.sh: --tool is required" >&2; exit 2; }
# The seed drives the before/after split once there is more than one tool. It is
# printed FIRST so a failing CI job reproduces exactly.
[ -n "$SEED" ] || SEED=${GITHUB_RUN_ID:-$(date +%s)}
[ -n "$WORK" ] || WORK=${TMPDIR:-/tmp}/keld-conformance

# Captured BEFORE isolate_init moves HOME: the tool binary lives in the real one.
REAL_HOME=$HOME
export REAL_HOME

LIB=$(cd "$(dirname "$0")/lib" && pwd)
# shellcheck source=lib/isolate.sh
. "$LIB/isolate.sh"
# shellcheck source=lib/tools.sh
. "$LIB/tools.sh"
# shellcheck source=lib/checkpoints.sh
. "$LIB/checkpoints.sh"

echo "seed: $SEED"
echo "tool: $TOOL   chain: $CHAIN   host: $(uname -s) $(uname -m)"

tool_supported "$TOOL" || { echo "run-chain.sh: $TOOL is not in the tool table yet" >&2; exit 2; }
[ "$CHAIN" = "A" ] || { echo "run-chain.sh: chain $CHAIN is not implemented yet (A only)" >&2; exit 2; }

trap 'teardown' EXIT

isolate_init "$WORK"
mocks_start
tool_install "$TOOL"
tool_env "$TOOL"

# --- step 1: the before half -------------------------------------------------
# The tool runs before Signal exists. No checkpoints: there is no daemon to have
# received anything, and asserting one here would be asserting nothing.
step_begin "before-signal"
tool_prompt "$TOOL" "before-signal"
[ -d "$(tool_transcript_root "$TOOL")" ] \
  || fail "the tool wrote no transcript root at $(tool_transcript_root "$TOOL")"

# --- step 2: onboard Signal --------------------------------------------------
signal_install
daemon_start

# --- step 3: the after half --------------------------------------------------
step_begin "after-signal"
tool_prompt "$TOOL" "after-signal"
step_end "${KELD_CONFORM_SETTLE:-60}"

echo
cat "$WORK/verdict-after-signal.json" | python3 -m json.tool 2>/dev/null | head -60
say "PASS — $(tool_display "$TOOL") $TOOL_VERSION_SEEN, chain $CHAIN, seed $SEED"
