#!/usr/bin/env bash
# Run one conformance chain against real tools, a mock model and a mock Atlas.
#
#   scripts/conformance/run-chain.sh --tool all [--chain A|B] [--seed N] [--work DIR]
#   scripts/conformance/run-chain.sh --tool claude_code
#   scripts/conformance/run-chain.sh --tool all --chain B --previous installed
#   scripts/conformance/run-chain.sh --tool all --artifact dir:./artifacts
#
# What it proves (AC-10): the packaged onboarding commands configure REAL tools,
# and the five lanes carry each tool's next prompt all the way to Atlas —
# transcript written, pointer received, store rows for the session, telemetry
# forwarded, enrichment or block published.
#
# What it never does: reach the network, read the developer's own ~/.keld,
# ~/.claude or ~/.codex, register a service, or keep any transcript but the ones
# our own scripted prompt produced.
#
# ⚠️ **ONE RUN COVERS BOTH ORDERS.** The tools are split by a SEEDED shuffle into
# a `before` half (installed and used before Signal exists) and an `after` half
# (installed after it, so the daemon's detector is what configures them). The
# seed is `--seed`, else $GITHUB_RUN_ID, else the epoch; it is the FIRST line of
# output and it rides every failure line, so a CI failure reproduces exactly.
# A one-tool list always lands in `before`, which is the original single-tool
# chain unchanged — `--before`/`--after` pin the halves explicitly when a run
# needs a particular tool in a particular role (a tool release runs chain A
# twice, once each way, per the spec).
#
# Chain A — a fresh install of the release under test:
#
#   before half installed + used  ->  Signal onboarded  ->  prompts (5 checkpoints)
#     ->  after half installed  ->  detector configures it  ->  prompts (5 checkpoints)
#
# Chain B — an upgrade over the previous release, and the reason it exists:
#
#   before half  ->  PREVIOUS release  ->  prompts  ->  the release under test
#     installed OVER it  ->  configs preserved, sidecar replaced, no version skew
#     ->  prompts  ->  after half  ->  detector  ->  prompts
#
# Chain B is the chain that would have caught the three-week sidecar skew
# (AGENTS.md → Gotchas): a 2.3.0 daemon against an Aug 11 sidecar, publishing
# telemetry and zero blocks while doctor reported no problems. So it does not
# merely assert the skew event is absent afterwards — an absence proves nothing
# on a machine where either half answers `dev`. It first runs the NEW daemon
# against the OLD sidecar deliberately and requires the event to FIRE.
#
# ⚠️ A FAILING STEP STOPS ITS CHAIN and every later step is reported SKIPPED,
# never failed: one break must read as one break.
#
# The container and VM legs (task A.4) run this same script where `install.sh`
# and the pkg can run for real (AC-12); this is the host case.
set -uo pipefail

TOOL=""
CHAIN="A"
SEED=""
WORK=""
BEFORE_ARG=""
AFTER_ARG=""
PREVIOUS="installed"
# Where the Signal under test comes from. `none` (the default) BUILDS FROM
# SOURCE and onboards with the same commands the installers call — the original
# behaviour, unchanged. `dir:<path>` is the AC-12 seam: a directory of REAL
# release artifacts, installed the unattended way by
# scripts/conformance/install-<os>.sh before the chain's own onboarding runs.
# Same shape as --previous, deliberately: one spec string, two forms, no new
# concept.
ARTIFACT="none"

while [ $# -gt 0 ]; do
  case "$1" in
    --tool)     TOOL=$2; shift 2 ;;
    --chain)    CHAIN=$2; shift 2 ;;
    --seed)     SEED=$2; shift 2 ;;
    --work)     WORK=$2; shift 2 ;;
    --before)   BEFORE_ARG=$2; shift 2 ;;
    --after)    AFTER_ARG=$2; shift 2 ;;
    --previous) PREVIOUS=$2; shift 2 ;;
    --artifact) ARTIFACT=$2; shift 2 ;;
    -h|--help)
      sed -n '2,48p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "run-chain.sh: unknown flag $1" >&2; exit 2 ;;
  esac
done

[ -n "$TOOL" ] || { echo "run-chain.sh: --tool is required (an id, a comma list, or 'all')" >&2; exit 2; }
[ -n "$SEED" ] || SEED=${GITHUB_RUN_ID:-$(date +%s)}
[ -n "$WORK" ] || WORK=${TMPDIR:-/tmp}/keld-conformance

# The version the binaries this run builds report as their own. It is not
# cosmetic: `version.Skew` answers `known=false` when either half says `dev`, so
# an unstamped build makes chain B's skew assertion vacuous — the exact shape of
# the outage chain B exists to catch.
UNDER_TEST_VERSION=${KELD_CONFORM_VERSION:-99.0.0-conform}

# Captured BEFORE isolate_init moves HOME: the tool binaries live in the real one.
REAL_HOME=$HOME
export REAL_HOME

LIB=$(cd "$(dirname "$0")/lib" && pwd)
# shellcheck source=lib/isolate.sh
. "$LIB/isolate.sh"
# shellcheck source=lib/tools.sh
. "$LIB/tools.sh"
# shellcheck source=lib/checkpoints.sh
. "$LIB/checkpoints.sh"

# ⚠️ FIRST LINE.
echo "seed: $SEED"
echo "chain: $CHAIN   host: $(uname -s) $(uname -m)"

case "$CHAIN" in
  A|B) ;;
  *) echo "run-chain.sh: unknown chain $CHAIN (A or B)" >&2; exit 2 ;;
esac

# --- the tool list and the seeded split --------------------------------------

if [ "$TOOL" = "all" ]; then
  TOOLS=$(tool_all)
else
  TOOLS=$(echo "$TOOL" | tr ',' ' ')
fi
for t in $TOOLS; do
  tool_supported "$t" || { echo "run-chain.sh: $t is not in the tool table yet" >&2; exit 2; }
done

# seeded_split <seed> <tool...> — two lines: the before half, then the after
# half. Deterministic in the seed alone, so the same seed reproduces the same
# pairing on any machine; a single tool always lands in `before`.
seeded_split() {
  local seed=$1; shift
  python3 - "$seed" "$@" <<'PY'
import random, sys
seed, tools = sys.argv[1], list(sys.argv[2:])
random.Random(seed).shuffle(tools)
cut = (len(tools) + 1) // 2
print(" ".join(tools[:cut]))
print(" ".join(tools[cut:]))
PY
}

if [ -n "$BEFORE_ARG$AFTER_ARG" ]; then
  BEFORE=$(echo "$BEFORE_ARG" | tr ',' ' ')
  AFTER=$(echo "$AFTER_ARG" | tr ',' ' ')
  SPLIT_KIND="pinned"
else
  SPLIT=$(seeded_split "$SEED" $TOOLS) || { echo "run-chain.sh: split failed" >&2; exit 2; }
  BEFORE=$(echo "$SPLIT" | sed -n 1p)
  AFTER=$(echo "$SPLIT" | sed -n 2p)
  SPLIT_KIND="seeded"
fi
echo "split ($SPLIT_KIND): before=[${BEFORE:-none}] after=[${AFTER:-none}]"

# Validate --artifact BEFORE the run starts. Resolution stays in the step (the
# artifacts have to exist when the installer runs, not now), but a typo in the
# spec must not cost a Go build, two npm installs and a headless prompt first.
case "$ARTIFACT" in
  none|dir:*) ;;
  *) echo "run-chain.sh: --artifact must be 'none' or 'dir:<path>' (got $ARTIFACT)" >&2; exit 2 ;;
esac
[ "$ARTIFACT" = "none" ] || echo "artifact: $ARTIFACT (the release under test is INSTALLED, not built — AC-12)"

trap 'teardown' EXIT

# --- the steps ---------------------------------------------------------------
#
# Each is a function; the chain is a list of them. The first one to fail stops
# the chain and the rest are reported SKIPPED.

SETTLE=${KELD_CONFORM_SETTLE:-60}
# AC-3 requires a tool installed after Signal to be LISTED within one detector
# poll, and integrations.DefaultPoll IS 60s — so a 60s budget is a coin flip on
# where the tick falls, not the criterion. Measured on a passing run: 37s. The
# budget is one poll plus slack and the harness does NOT shorten
# KELD_INTEGRATIONS_POLL, because a run that shortened it would stop proving the
# number AC-3 is written about.
DETECT_BUDGET=${KELD_CONFORM_DETECT:-90}

# The previous release's step does not REQUIRE store_rows. That lane's
# capability belongs to the sidecar the previous release shipped, and a release
# that predates a reader cannot be failed for not having it — while every other
# lane is required, and after the upgrade all five are. That asymmetry is chain
# B's whole point rendered as an expectation: the lane the old sidecar cannot
# serve is the lane the upgrade must repair.
PREV_NOT_EXPECTED=store_rows

step_before_install() {
  step_begin "before-install"
  local t
  for t in $BEFORE; do
    tool_materialize "$t"
    tool_prompt "$t" "before-install"
  done
  # No checkpoints: there is no daemon to have received anything, and asserting
  # one here would be asserting nothing.
  return 0
}

step_signal_install() {
  step_begin "signal-install"
  # AC-12, when the run was given real artifacts: install them the unattended
  # way and verify the machine from observed state. A no-op under the default
  # `--artifact none`, so every existing invocation is byte-for-byte unchanged.
  artifact_install || return 1
  signal_install
  daemon_start
  return 0
}

step_before_prompts() {
  step_begin "after-signal"
  local t
  for t in $BEFORE; do tool_prompt "$t" "after-signal"; done
  step_assert "$SETTLE" "" $BEFORE
}

step_detect() {
  step_begin "detect"
  [ -n "$AFTER" ] || { say "no tools in the after half — the detector has nothing to find"; return 0; }
  local t
  for t in $AFTER; do
    say "installing $(tool_display "$t") AFTER Signal; the detector has ${DETECT_BUDGET}s to configure it"
    tool_materialize "$t"
  done
  for t in $AFTER; do
    tool_await_configured "$t" "$DETECT_BUDGET" || return 1
  done
  return 0
}

step_after_prompts() {
  step_begin "after-detect"
  [ -n "$AFTER" ] || { say "no tools in the after half — nothing to prompt"; return 0; }
  local t
  for t in $AFTER; do tool_prompt "$t" "after-detect"; done
  step_assert "$SETTLE" "" $AFTER
}

# --- chain B's own steps -----------------------------------------------------

step_prev_install() {
  step_begin "prev-install"
  prev_resolve "$PREVIOUS" || return 1
  bin_use "$PREV_BIN_DIR"
  sidecar_point_at "$PREV_SIDECAR_DIR"
  say "previous release: keld $KELD_VERSION_SEEN, sidecar $SIDECAR_VERSION_SEEN (--previous $PREVIOUS)"
  PREV_SIDECAR_VERSION=$SIDECAR_VERSION_SEEN
  signal_install
  daemon_start
  return 0
}

step_prev_prompts() {
  step_begin "prev-prompts"
  local t
  for t in $BEFORE; do tool_prompt "$t" "prev-prompts"; done
  step_assert "$SETTLE" "$PREV_NOT_EXPECTED" $BEFORE || return 1
  # The configs the upgrade must preserve, byte for byte, hashed now.
  config_snapshot
  return 0
}

# step_skew_control — the NEW daemon against the OLD sidecar, deliberately.
#
# ⚠️ This state is artificial: a real upgrade replaces both halves at once. It
# is inserted because the assertion that matters — "no sidecar.version_skew
# after the upgrade" — is an ABSENCE, and an absence proves nothing unless the
# detector is known to fire when the condition holds. A machine whose halves
# both answer `dev` reports no skew forever, which is indistinguishable from a
# working check. So: install the new binaries, leave the old sidecar, and
# require the event. KELD_CONFORM_SKEW_CONTROL=0 skips it.
step_skew_control() {
  step_begin "skew-control"
  daemon_stop
  bin_use "$WORK/bin-under-test"
  if [ "${KELD_CONFORM_SKEW_CONTROL:-1}" != "1" ]; then
    say "skew control skipped (KELD_CONFORM_SKEW_CONTROL=0)"
    return 0
  fi
  if [ "$PREV_SIDECAR_VERSION" = "dev" ] || [ "$UNDER_TEST_VERSION" = "dev" ]; then
    say "skew control INCONCLUSIVE: daemon=$UNDER_TEST_VERSION sidecar=$PREV_SIDECAR_VERSION —"
    say "  a 'dev' half means CANNOT TELL (version.Skew known=false), so neither the"
    say "  presence nor the absence of sidecar.version_skew says anything here."
    return 0
  fi
  say "new daemon ($UNDER_TEST_VERSION) against the OLD sidecar ($PREV_SIDECAR_VERSION) — the event must fire"
  daemon_start
  if ! await_clientevent "sidecar.version_skew" 45; then
    say "no sidecar.version_skew reached the mock Atlas. The detector this chain"
    say "  depends on is silent on a machine whose halves genuinely disagree —"
    say "  which is the three-week outage, not a harness detail."
    return 1
  fi
  say "sidecar.version_skew fired, as it must — the detector works on this machine"
  SKEW_EVENTS_BEFORE_UPGRADE=$(clientevent_count "sidecar.version_skew")
  return 0
}

step_upgrade() {
  step_begin "upgrade"
  daemon_stop
  bin_use "$WORK/bin-under-test"
  sidecar_point_at "$UNDER_TEST_SIDECAR"
  daemon_start

  config_unchanged || return 1

  if [ "$SIDECAR_VERSION_SEEN" = "$PREV_SIDECAR_VERSION" ]; then
    say "the sidecar was NOT replaced: both halves report $SIDECAR_VERSION_SEEN."
    say "  An upgrade that leaves the old analysis half in place is exactly the"
    say "  state that published telemetry and zero blocks for three weeks."
    return 1
  fi
  say "sidecar replaced: $PREV_SIDECAR_VERSION -> $SIDECAR_VERSION_SEEN"

  local after
  after=$(clientevent_count "sidecar.version_skew")
  if [ "$after" -gt "${SKEW_EVENTS_BEFORE_UPGRADE:-0}" ]; then
    say "sidecar.version_skew fired AFTER the upgrade ($after total): the two halves"
    say "  still disagree."
    return 1
  fi
  if [ "$SIDECAR_VERSION_SEEN" = "dev" ] || [ "$UNDER_TEST_VERSION" = "dev" ]; then
    say "no sidecar.version_skew after the upgrade — but INCONCLUSIVE: the new"
    say "  sidecar reports '$SIDECAR_VERSION_SEEN' and a 'dev' half means CANNOT TELL."
    say "  Only a frozen, stamped sidecar makes this assertion say anything."
  else
    say "no sidecar.version_skew after the upgrade ($UNDER_TEST_VERSION vs $SIDECAR_VERSION_SEEN)"
  fi
  return 0
}

step_upgraded_prompts() {
  step_begin "upgraded-prompts"
  local t
  for t in $BEFORE; do tool_prompt "$t" "upgraded-prompts"; done
  step_assert "$SETTLE" "" $BEFORE
}

# --- config preservation -----------------------------------------------------

# config_snapshot — hash every tool config file the chain has configured.
config_snapshot() {
  CONFIG_HASHES="$WORK/config-hashes.txt"
  : > "$CONFIG_HASHES"
  local t p
  for t in $BEFORE $AFTER; do
    p=$(tool_config_path "$t")
    [ -f "$p" ] && shasum -a 256 "$p" >> "$CONFIG_HASHES"
  done
  say "config snapshot: $(wc -l < "$CONFIG_HASHES" | tr -d ' ') file(s)"
}

# config_unchanged — the upgrade must not rewrite a tool's config. AC-12's
# quiet half: an upgrade that re-runs setup would also re-point a tool whose
# owner had edited it.
config_unchanged() {
  [ -f "${CONFIG_HASHES:-}" ] || { say "no config snapshot to compare"; return 0; }
  if shasum -a 256 -c "$CONFIG_HASHES" >"$WORK/config-check.out" 2>&1; then
    say "tool configs preserved byte for byte across the upgrade"
    return 0
  fi
  say "a tool config CHANGED across the upgrade:"
  cat "$WORK/config-check.out" >&2
  return 1
}

# --- the previous release ----------------------------------------------------

# prev_resolve <spec> — where chain B's "previous release" comes from.
#
# Two honest options and both are parameters, because neither is right
# everywhere:
#
#   installed   (default) the release already on THIS machine — the pkg's
#               /usr/local/keld or ~/.local/bin, plus the frozen sidecar beside
#               it. It is a real, shipped pair, which is what makes the skew
#               assertion meaningful; it is whatever this developer happens to
#               have, which is what makes it unreproducible in CI.
#   dir:<path>  a directory holding keld, keld-agent and keld-agent-sidecar/.
#               This is the seam CI uses: the workflow downloads the previous
#               release's artifacts and points the chain at them, so the run
#               names a version rather than a machine.
#
# Deliberately NOT implemented here: fetching a GitHub release. That belongs in
# the workflow that already has the token and the artifact names, and inventing
# a second downloader here would be a network path nothing local can test.
prev_resolve() {
  local spec=$1
  case "$spec" in
    dir:*)
      local d=${spec#dir:}
      [ -x "$d/keld-agent" ] || { say "no keld-agent under $d"; return 1; }
      PREV_BIN_DIR=$d
      PREV_SIDECAR_DIR="$d/keld-agent-sidecar"
      ;;
    installed)
      PREV_BIN_DIR=""
      local d
      for d in "/usr/local/keld" "$REAL_HOME/.local/bin" "/usr/local/bin"; do
        if [ -x "$d/keld-agent" ] && [ -x "$d/keld" ]; then PREV_BIN_DIR=$d; break; fi
      done
      [ -n "$PREV_BIN_DIR" ] || {
        say "no installed Signal release found (looked in /usr/local/keld, ~/.local/bin, /usr/local/bin)."
        say "  Pass --previous dir:<path> with a downloaded release instead."
        return 1; }
      PREV_SIDECAR_DIR=""
      for d in "$REAL_HOME/.local/bin/keld-agent-sidecar" "/usr/local/keld/keld-agent-sidecar"; do
        [ -x "$d/keld-agent-sidecar" ] && { PREV_SIDECAR_DIR=$d; break; }
      done
      [ -n "$PREV_SIDECAR_DIR" ] || {
        say "the installed release has no frozen sidecar; chain B cannot compare the two halves."
        return 1; }
      ;;
    *) say "--previous must be 'installed' or 'dir:<path>' (got $spec)"; return 1 ;;
  esac
  return 0
}

# --- run ---------------------------------------------------------------------

isolate_init "$WORK"
mocks_start
for t in $TOOLS; do
  tool_install "$t"
  tool_env "$t"
done

# The sidecar the release under test ships. `worktree` is the venv running this
# checkout's serve.py — the only "new" sidecar a local run has.
UNDER_TEST_SIDECAR=${KELD_CONFORM_NEW_SIDECAR:-worktree}

if [ "$CHAIN" = "A" ]; then
  STEPS="step_before_install step_signal_install step_before_prompts step_detect step_after_prompts"
else
  STEPS="step_before_install step_prev_install step_prev_prompts step_skew_control step_upgrade step_upgraded_prompts step_detect step_after_prompts"
fi

FAILED_STEP=""
for fn in $STEPS; do
  if [ -n "$FAILED_STEP" ]; then
    echo "conformance: SKIPPED ${fn#step_} (chain stopped at $FAILED_STEP, seed $SEED)"
    continue
  fi
  if ! "$fn"; then
    FAILED_STEP=$STEP
    echo "conformance: chain $CHAIN stopped at step $FAILED_STEP [seed $SEED]" >&2
  fi
done

echo
if [ -n "$FAILED_STEP" ]; then
  say "FAIL — chain $CHAIN, seed $SEED, stopped at step $FAILED_STEP"
  exit 1
fi
say "PASS — chain $CHAIN, seed $SEED, before=[${BEFORE:-none}] after=[${AFTER:-none}]"
for t in $TOOLS; do say "  $(tool_display "$t") $(tool_version_of "$t")"; done
