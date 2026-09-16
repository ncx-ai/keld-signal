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

# ⚠️ **ON WINDOWS THE PREVIOUS RELEASE CANNOT ENRICH AT ALL, and that is a
# PRODUCT fact this branch just fixed rather than a harness gap.** Every keld
# up to and including v3.0.3 writes its hook command as a BARE path, and on
# Windows a bare path is a run of backslash escapes that leaves nothing
# executable behind — so no hook ever fires, no pointer ever reaches the daemon,
# and `pointer`/`publish` are dark. Measured on windows-latest 2026-09-16, and
# proven rather than inferred: a control hook added beside keld's own FIRED, so
# the tool runs hooks and keld's command was the thing that did not work.
#
# That is exactly what chain B exists to express. The lane the OLD release
# cannot serve is the lane the UPGRADE must repair — the same asymmetry
# store_rows already encodes one line up — so the expectation is widened here
# and the post-upgrade step still requires ALL FIVE. If the upgrade did not fix
# it, `upgraded-prompts` fails and this expectation cannot hide it.
#
# ⚠️ REMOVE THIS the release after the quoting fix ships. Once the previous
# release is one that CAN enrich on Windows, keeping it would let a real
# regression pass unnoticed — an expectation that outlives its reason is
# indistinguishable from a silenced test.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) PREV_NOT_EXPECTED="$PREV_NOT_EXPECTED,pointer,publish" ;;
esac

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
  # The hook control is read HERE, right after the prompt that should have
  # triggered it, and reported whether the step passes or fails — a control
  # that only speaks on failure cannot establish that it works when things
  # are fine, which is the whole point of having one.
  local rc=0
  step_assert "$SETTLE" "" $BEFORE || rc=1
  hook_control_report
  return $rc
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

# await_hook_repair — the detector repairs a broken hook command on its own
# poll, so the chain must wait for it exactly as a real machine's next prompt
# would.
#
# ⚠️ **WITHOUT THIS THE REPAIR IS UNTESTABLE, not merely slow.** The prompt
# happens ONCE and then step_assert only re-reads checkpoints; a hook repaired
# after the prompt cannot retroactively produce a pointer for it. So a run that
# prompted immediately would fail for its whole budget with the fix working
# perfectly.
#
# It waits on the CONFIG, not on a timer: the repair is done when the tool's own
# config no longer holds a command that cannot run. DETECT_BUDGET is the same
# one poll plus slack AC-3 is measured against, and KELD_INTEGRATIONS_POLL is
# NOT shortened — a run that shortened it would stop proving the number.
await_hook_repair() {
  local t p budget=$DETECT_BUDGET i
  for t in $BEFORE $AFTER; do
    p=$(tool_config_path "$t")
    [ -f "$p" ] || continue
    for i in $(seq 1 "$budget"); do
      if ! grep -qE '"[^"]*[\\ ][^"]*__hook --source' "$p" 2>/dev/null; then
        [ "$i" -gt 1 ] && say "hook command repaired for $t after ${i}s"
        break
      fi
      sleep 1
    done
  done
}

step_upgraded_prompts() {
  step_begin "upgraded-prompts"
  await_hook_repair
  local t
  for t in $BEFORE; do tool_prompt "$t" "upgraded-prompts"; done
  step_assert "$SETTLE" "" $BEFORE
}

# --- config preservation -----------------------------------------------------

# sha256_tool — how to hash on THIS machine.
#
# ⚠️ **`shasum` DOES NOT EXIST IN GIT BASH.** It is a Perl script shipped by
# macOS and most Linux distributions; Git for Windows bundles coreutils, which
# provides `sha256sum` and not `shasum`. The config check called `shasum`
# unconditionally, so on Windows it failed with "command not found" and the
# chain reported "a tool config CHANGED across the upgrade" — a confident claim
# about the product derived from a missing tool, and exactly the wrong sentence
# to put in front of whoever reads that failure.
#
# Resolved once, and the run SAYS which it picked, because a checksum that
# silently changes implementation between platforms is worth stating.
SHA256_CMD=""
sha256_resolve() {
  [ -n "$SHA256_CMD" ] && return 0
  if command -v sha256sum >/dev/null 2>&1; then SHA256_CMD=sha256sum
  elif command -v shasum >/dev/null 2>&1; then SHA256_CMD="shasum -a 256"
  else fail "no sha256sum and no shasum on PATH; the config-preservation check cannot run"
  fi
  say "config hashing with: $SHA256_CMD"
}

# config_snapshot — hash every tool config file the chain has configured.
config_snapshot() {
  sha256_resolve
  CONFIG_HASHES="$WORK/config-hashes.txt"
  : > "$CONFIG_HASHES"
  rm -f "$CONFIG_HASHES".*
  local t p
  for t in $BEFORE $AFTER; do
    p=$(tool_config_path "$t")
    if [ -f "$p" ]; then
      config_normalise "$p" > "$WORK/config-before-$t.txt"
      $SHA256_CMD < "$WORK/config-before-$t.txt" > "$CONFIG_HASHES.$t"
    fi
  done
  say "config snapshot: $(wc -l < "$CONFIG_HASHES" | tr -d ' ') file(s)"
}

# config_unchanged — the upgrade must not rewrite a tool's config. AC-12's
# quiet half: an upgrade that re-runs setup would also re-point a tool whose
# owner had edited it.
config_unchanged() {
  [ -f "${CONFIG_HASHES:-}" ] || { say "no config snapshot to compare"; return 0; }
  sha256_resolve
  local t p before now ok=1
  for t in $BEFORE $AFTER; do
    [ -f "$CONFIG_HASHES.$t" ] || continue
    p=$(tool_config_path "$t")
    [ -f "$p" ] || { say "$t's config DISAPPEARED across the upgrade"; ok=0; continue; }
    before=$(cat "$CONFIG_HASHES.$t")
    now=$(config_normalise "$p" | $SHA256_CMD)
    if [ "$before" != "$now" ]; then
      say "$t's config CHANGED across the upgrade, outside the lines keld owns:"
      # ⚠️ PRINT THE DIFF. Two rounds were spent widening the blanking list by
      # guessing at what else had moved, on a message that named only what it
      # was NOT. A check that reports a mismatch without showing it costs a full
      # CI round trip per guess — the same lesson the npm shim resolver learned.
      config_normalise "$p" > "$WORK/config-now-$t.txt"
      if [ -f "$WORK/config-before-$t.txt" ]; then
        diff -u "$WORK/config-before-$t.txt" "$WORK/config-now-$t.txt" \
          | head -40 | sed 's/^/    /' >&2
      fi
      ok=0
    fi
  done
  if [ "$ok" = "1" ]; then
    say "tool configs preserved across the upgrade (keld's own hook command aside)"
    return 0
  fi
  return 1
}

# config_normalise — the config with keld's own hook COMMAND blanked.
#
# ⚠️ **"PRESERVED BYTE FOR BYTE" CANNOT SURVIVE A SELF-REPAIR, and the
# assertion it was protecting is not the one that matters.** The detector now
# rewrites a hook command that cannot execute — that IS the fix — and the new
# command also names the new binary, so on a machine that needed repairing the
# file legitimately differs in two ways at once.
#
# What the check exists to catch is an upgrade trampling a config its OWNER
# edited: a re-pointed endpoint, a dropped env block, a lost unrelated hook.
# Blanking keld's own command value keeps every one of those failing while
# letting the repair through, which is strictly MORE precise than byte
# equality — under the old rule a repair and a trampling were the same event.
config_normalise() {
  # Any LINE carrying keld's hook command is keld's own — Claude Code's JSON
  # object and Codex's TOML inline table both put the whole entry on one line —
  # so the line is replaced wholesale. A regex over the command VALUE was tried
  # first and could not see both shapes at once: the repaired form arrives with
  # its inner quotes ESCAPED inside the JSON string, so "quoted" and "bare"
  # differ in more than a pair of characters.
  python3 -c '
import sys
# Every line keld OWNS, and nothing else. A self-repair re-runs the adapter,
# which rewrites the whole managed block — not just the hook command — and the
# telemetry endpoint legitimately moves because the restarted daemon picked a
# new loopback port. Blanking only the hook line left that endpoint behind and
# the check reported a change "outside keld|s own hook command", which was true
# and still not a trampling.
OWNED = (
    "__hook --source",              # the hook command itself
    "OTEL_",                        # endpoint, protocol, exporters, headers
    "CLAUDE_CODE_ENABLE_TELEMETRY", # the switch that turns the above on
    "x-keld-ingest-token",          # the loopback secret, inside the headers
)
for line in open(sys.argv[1], encoding="utf-8", errors="replace"):
    sys.stdout.write("<KELD_OWNED_LINE>\n" if any(k in line for k in OWNED) else line)
' "$1"
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
      local agent=keld-agent
      case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) agent=keld-agent.exe ;; esac
      [ -f "$d/$agent" ] || { say "no $agent under $d"; return 1; }
      PREV_BIN_DIR=$d
      # ⚠️ **TWO LAYOUTS, BECAUSE WINDOWS SHIPS NO SIDECAR ARCHIVE.** Every
      # other platform publishes keld-agent-sidecar_<os>_<arch>.tar.gz, which
      # unpacks into its own keld-agent-sidecar/ directory. Windows publishes
      # the sidecar ONLY inside keld-setup.exe, whose [Files] line lays it FLAT
      # into {localappdata}\Programs\keld beside keld.exe — so a "previous
      # release" assembled on Windows has the sidecar binary as a SIBLING of
      # keld-agent.exe rather than one directory down.
      #
      # Detected by LOOKING, not from uname: a caller may hand over a tree it
      # unpacked itself in either shape, and inferring from the OS would be
      # wrong for that caller on the platform where it matters least.
      if [ -d "$d/keld-agent-sidecar" ]; then
        PREV_SIDECAR_DIR="$d/keld-agent-sidecar"
      else
        PREV_SIDECAR_DIR="$d"
      fi
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
