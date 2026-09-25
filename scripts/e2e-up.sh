#!/bin/bash
# Bring up an ISOLATED Keld Signal daemon over a generated corpus for the
# Playwright suite in ui/e2e/. Nothing here touches ~/.keld or the network:
# HOME and KELD_HOME are both overridden (KELD_WATCH_ROOTS only ADDS a root,
# so leaving HOME alone would make the watcher read the developer's own
# transcripts), and the daemon runs with KELD_ATLAS=0.
#
# Usage: scripts/e2e-up.sh <workdir>
#
# On success <workdir>/state.json holds everything the suite needs:
#   {"baseURL","secret","port","daemonPid","pgid","sidecarPidFile",
#    "home","work","repoRoot","log","settingsMounted","integrationsMounted",
#    "blocks"}
# and the daemon (plus its sidecar) is left RUNNING, in the process group
# this script was started in, for ui/e2e/global-teardown.ts to reap.
#
# Steps: generate corpus -> build daemon -> start daemon + worktree sidecar
#        -> wait until /v1/ledger holds >= 1 block -> write state.json.
set -u

WORK=${1:?usage: e2e-up.sh <workdir>}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$WORK"
WORK=$(cd "$WORK" && pwd)

HOME_DIR="$WORK/home"
SIM="$WORK/corpus"
WORKSPACES="$WORK/workspaces"
LOG="$WORK/daemon.log"
# The venv `make sidecar` creates; the frozen sidecar an installer put on
# this machine may be OLDER than the worktree, so the suite always runs the
# worktree's own sidecar/serve.py under that interpreter.
PY=${KELD_E2E_PYTHON:-$HOME/.keld/sidecar-venv/bin/python}
# Seed 1 was chosen by scanning: 5 sessions over 4 repositories, 21 blocks,
# and THREE sessions with an idle gap >= 15 minutes, so the "Show breaks"
# journey has a break to reveal whichever session's blocks are cut first.
SEED=${KELD_E2E_SEED:-1}
SESSIONS=${KELD_E2E_SESSIONS:-5}
# The corpus's last event is anchored 30 minutes ago: every intended block is
# then closed (15 minutes of quiet has already passed) and everything falls in
# the current week, which /v1/projects' coverage tile is scoped to. The STRUCTURE
# (sessions, repos, tokens, run/break shape) is identical for a given seed
# whatever the anchor; only the clock times move, and the visual baseline masks
# those. Set KELD_E2E_END to an ISO instant to pin them too.
END=${KELD_E2E_END:-$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.UTC)-datetime.timedelta(minutes=30)).replace(microsecond=0).isoformat().replace("+00:00","Z"))')}
READY_TIMEOUT=${KELD_E2E_READY_TIMEOUT:-240}
# After the first block lands, how long to wait for the REST of the corpus's
# intended blocks before proceeding anyway (the specs assert on what they need
# and fail honestly if it never arrived).
SETTLE_TIMEOUT=${KELD_E2E_SETTLE_TIMEOUT:-150}

fail() { echo "e2e-up: FAIL: $*" >&2; [ -f "$LOG" ] && tail -40 "$LOG" >&2; exit 1; }

[ -x "$PY" ] || fail "no sidecar interpreter at $PY (run 'make sidecar', or set KELD_E2E_PYTHON)"
command -v go >/dev/null || fail "go toolchain not on PATH"
command -v python3 >/dev/null || fail "python3 not on PATH (the generator needs it)"

rm -rf "$HOME_DIR" "$SIM" "$WORKSPACES" "$WORK/state.json" "$WORK/sidecar.pid"
mkdir -p "$HOME_DIR/state"

echo "e2e-up: generating corpus (seed $SEED, $SESSIONS sessions, ending $END)"
python3 "$ROOT/scripts/blockgen/blockgen.py" --out "$SIM" --workspaces "$WORKSPACES" \
  --sessions "$SESSIONS" --days 1 --seed "$SEED" --end "$END" >"$WORK/blockgen.log" 2>&1 \
  || fail "blockgen failed: $(tail -5 "$WORK/blockgen.log")"
INTENDED=$(python3 -c "import json;m=json.load(open('$SIM/manifest.json'));print(sum(r['n_blocks'] for s in m['session_list'] for r in s['runs']))")
echo "e2e-up: corpus intends $INTENDED blocks"

echo "e2e-up: building keld-agent"
(cd "$ROOT" && go build -o "$WORK/keld-agent" ./cmd/keld-agent) || fail "go build failed"

# Minimal hook.json: the daemon idles without one, and with KELD_ATLAS=0 the
# token is never used. The endpoint is a dead loopback port on purpose.
cat > "$HOME_DIR/hook.json" <<JSON
{"ingest_token":"e2e-local-only","endpoint":"http://127.0.0.1:9/unused"}
JSON

# What a fresh install lands on (settings.WriteInstallDefaults): the model-free
# facet set plus the block emitter. Under the compiled-in "auto" default the
# first enrichment job downloads ~1.9 GB of GLiNER2 weights into this HOME —
# the one network access the suite must never make.
#
# ⚠️ **`auto_setup_integrations` IS OFF, AND IT IS SET IN THE FILE RATHER THAN
# THE ENVIRONMENT ON PURPOSE.** Two reasons, and neither is a preference.
#
# OFF because the detector writes REAL tool configs, through the same adapters
# and the same write path `keld signal setup` uses, into whatever HOME it is
# given. The safe default for a fixture machine is that nothing is configured
# until a spec has decided it should be: AC-2's Set up journey needs the Codex
# row to still be `not_configured` when the button is pressed, and a detector
# that got there first leaves nothing to click.
#
# IN THE FILE because `Settings.AutoSetupEnabled` resolves env > file > ON, and
# the detector re-reads it LIVE on every tick (`settings.Load()` opens
# agent-config.json per call and caches nothing). So an env var would freeze the
# toggle for the daemon's whole life, while the file stays the live lever — which
# is how AC-2 (needs OFF) and AC-3 (needs ON) share ONE fixture daemon instead of
# paying a second three-minute bring-up. `integrations.spec.ts` flips this key and
# the next poll honours it, exactly as the pane's own toggle would.
cat > "$HOME_DIR/agent-config.json" <<JSON
{"ml_backend":"deterministic","blocks":true,"auto_setup_integrations":false}
JSON

# An empty project document in 3.0.6's stored shape, with the one group that
# shape needs (the page has no groups since 2026-09-25; the file keeps one so
# a 3.0.6 rollback renders every project). No projects yet, so every
# repository starts out as a suggestion.
# 3.0.6's stored shape: groups under `workstreams` (vocab:keep).
cat > "$HOME_DIR/state/projects.json" <<JSON
{"version":1,
 "workstreams":[{"key":"development","name":"Development","question":"Which project is this work for?","template_id":"project","origin":"local","off":false}],
 "projects":[]}
JSON

# The wrapper records its own pid before exec so teardown can reap the
# sidecar's process GROUP (the supervisor gives it one via Setpgid) even if
# the daemon is already gone.
cat > "$WORK/sidecar-wrapper" <<SH
#!/bin/sh
echo \$\$ > "$WORK/sidecar.pid"
exec "$PY" "$ROOT/sidecar/serve.py" "\$@"
SH
chmod +x "$WORK/sidecar-wrapper"

TELEMETRY_PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')

export HOME="$HOME_DIR"
export KELD_HOME="$HOME_DIR"
export KELD_ATLAS=0
export KELD_BLOCKS=1
export KELD_BLOCKS_INTERVAL=20s
export KELD_BLOCKS_BACKFILL=1
export KELD_WATCH_ROOTS="claude_code:$SIM"
export KELD_WATCH_POLL=2s
# ⚠️ BACKFILL ON, and the default matters. The corpus is STATIC HISTORY written
# before the daemon starts, so nothing ever "grows" for a forward-only watcher to
# notice. Measured with it off: 1 of 5 transcripts was ever ingested, one
# repository resolved, and the Projects pane had a single suggestion — which the
# serial specs then consumed, leaving the next one nothing to place. A real
# machine sees transcripts appear while it runs; a generated corpus does not, and
# backfill is the mode that reads what is already on disk.
export KELD_WATCH_BACKFILL=${KELD_WATCH_BACKFILL:-1}
export KELD_TELEMETRY_PORT="$TELEMETRY_PORT"
export KELD_SIDECAR_BIN="$WORK/sidecar-wrapper"
# named_terms needs spaCy (~619 MB) and nothing the page shows reads it.
export KELD_TERMS=${KELD_TERMS:-0}

echo "e2e-up: starting daemon (home $HOME_DIR, telemetry port $TELEMETRY_PORT)"
"$WORK/keld-agent" run >"$LOG" 2>&1 &
DAEMON_PID=$!
echo "$DAEMON_PID" > "$WORK/daemon.pid"

for _ in $(seq 1 60); do
  [ -f "$HOME_DIR/agent.json" ] && break
  kill -0 "$DAEMON_PID" 2>/dev/null || fail "daemon exited early"
  sleep 1
done
[ -f "$HOME_DIR/agent.json" ] || fail "no agent.json after 60s"
PORT=$(python3 -c "import json;print(json.load(open('$HOME_DIR/agent.json'))['port'])")
SECRET=$(python3 -c "import json;print(json.load(open('$HOME_DIR/agent.json'))['secret'])")
BASE="http://127.0.0.1:$PORT"
echo "e2e-up: daemon on $BASE"

echo "e2e-up: waiting for blocks in /v1/ledger (up to ${READY_TIMEOUT}s)"
N=0
DEADLINE=$((SECONDS + READY_TIMEOUT))
while [ $SECONDS -lt $DEADLINE ]; do
  kill -0 "$DAEMON_PID" 2>/dev/null || fail "daemon died while waiting for blocks"
  N=$(curl -s -H "x-keld-agent-secret: $SECRET" "$BASE/v1/ledger" | python3 -c '
import json,sys
try: print(len(json.load(sys.stdin).get("blocks") or []))
except Exception: print(0)' 2>/dev/null)
  [ "${N:-0}" -gt 0 ] && break
  sleep 2
done
[ "${N:-0}" -gt 0 ] || fail "no blocks after ${READY_TIMEOUT}s"
# ⚠️ **SETTLE ON A COUNT THAT HAS STOPPED MOVING, NOT ON THE FIRST MOMENT IT
# REACHES $INTENDED.** This loop exited as soon as the ledger held $INTENDED
# blocks — while more were still arriving. The number it exits on is therefore
# not the number the page shows a minute later, and the visual baseline is a
# screenshot of the page a minute later.
#
# MEASURED: `visual.spec.ts` failed on roughly half of full-suite runs and
# passed every time it ran alone, with the failing screenshot ~190px taller
# (about two extra rows). A solo run screenshots seconds after bring-up; the
# full suite reaches the same test after fourteen other tests, by which time the
# late blocks have landed. A baseline that depends on how long the specs before
# it took is not a baseline, and the failure looked like flakiness rather than
# like a race the harness owned.
#
# So: wait for the count to be UNCHANGED across three consecutive polls, with
# $INTENDED as a FLOOR rather than a target — the floor is what stops an early
# plateau (the first session ingested, the rest still going) from reading as
# settled. Report when it settles above the floor, since the cutter closing a
# trailing block the generator did not lay out is a legitimate reason for the
# two numbers to differ and the baseline is captured against the real one.
echo "e2e-up: first block landed after $((SECONDS - (DEADLINE - READY_TIMEOUT)))s; settling (>= $INTENDED, until the count stops moving, up to ${SETTLE_TIMEOUT}s)"
DEADLINE=$((SECONDS + SETTLE_TIMEOUT))
STABLE=0
PREV=-1
while [ $SECONDS -lt $DEADLINE ]; do
  sleep 3
  N=$(curl -s -H "x-keld-agent-secret: $SECRET" "$BASE/v1/ledger" | python3 -c '
import json,sys
try: print(len(json.load(sys.stdin).get("blocks") or []))
except Exception: print(0)' 2>/dev/null)
  if [ "${N:-0}" = "$PREV" ]; then
    STABLE=$((STABLE + 1))
  else
    STABLE=0
  fi
  PREV=${N:-0}
  # Three unchanged polls (~9s) AND at least what the generator laid out. The
  # floor matters: an early plateau while the first session is still ingesting
  # would otherwise look settled.
  [ "$STABLE" -ge 3 ] && [ "${N:-0}" -ge "$INTENDED" ] && break
done
if [ "${N:-0}" -gt "$INTENDED" ]; then
  echo "e2e-up: $N blocks in the ledger ($INTENDED laid out by the generator, plus $((N - INTENDED)) trailing block(s) the cutter closed on idle)"
else
  echo "e2e-up: $N of $INTENDED intended blocks in the ledger"
fi

# ⚠️ **THEN WAIT FOR THE REPOSITORY TO RESOLVE, WHICH IS NOT THE SAME AS
# WAITING FOR BLOCKS.** A block's dims are recorded once, at cut time, and never
# revised — so a block cut before the sidecar has finished resolving the
# checkout's workspace carries no `repo` dim FOREVER, and the Projects pane
# groups it under the directory name instead of the remote. Measured: the same
# corpus produced a repo-keyed suggestion on one run and a workspace-keyed one
# on the next, purely on ordering, which made the projects spec flaky.
#
# So the harness waits for the state the product is actually meant to be in
# rather than asserting against whichever race it lost. A corpus of real git
# checkouts that NEVER resolves a repository is a genuine bug, so a timeout here
# fails loudly instead of proceeding: a spec passing against workspace-keyed
# suggestions would be asserting the wrong thing quietly.
echo "e2e-up: waiting for the repository dimension to resolve (up to ${SETTLE_TIMEOUT}s)"
DEADLINE=$((SECONDS + SETTLE_TIMEOUT))
REPO_KEYED=0
while [ $SECONDS -lt $DEADLINE ]; do
  REPO_KEYED=$(curl -s -H "x-keld-agent-secret: $SECRET" "$BASE/v1/projects" | python3 -c '
import json,sys
try: print(sum(1 for s in (json.load(sys.stdin).get("suggestions") or []) if s.get("kind") == "repo"))
except Exception: print(0)' 2>/dev/null)
  [ "${REPO_KEYED:-0}" -gt 0 ] && break
  sleep 3
done
[ "${REPO_KEYED:-0}" -gt 0 ] \
  || fail "no repository-keyed suggestion after ${SETTLE_TIMEOUT}s — the generated checkouts under $WORKSPACES did not resolve to a remote, so every block grouped by directory name instead. Check blockgen's .git/config and sidecar workspace resolution before trusting any spec."
echo "e2e-up: repository resolved ($REPO_KEYED repo-keyed suggestion(s))"

# Is B2's settings route mounted on this build? The specs that need it skip
# with a reason when it is not, rather than failing on a lane still in flight.
SETTINGS_CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "x-keld-agent-secret: $SECRET" "$BASE/v1/settings")
SETTINGS_MOUNTED=False; [ "$SETTINGS_CODE" = "200" ] && SETTINGS_MOUNTED=True

# ⚠️ **PROVE HOME ISOLATION REACHED THE DETECTOR BEFORE ANY SPEC WRITES A FILE.**
# The integrations catalogue resolves `~/.codex`, `~/.claude`, `~/.gemini` and
# `~/.cursor` through `os.UserHomeDir()` — i.e. `$HOME` — at CALL time, and so do
# all three tool adapters and `watch.DiscoverRoots()`. The export above is
# therefore sufficient, and that was verified by reading the same values twice in
# one process with HOME moved between the reads.
#
# But "sufficient today" is exactly the thing that stops being true quietly, and
# the cost of it stopping is not a failed test: `ui/e2e/integrations.spec.ts`
# CREATES `<HOME>/.codex/config.toml` and lets the daemon's own adapter rewrite
# it. If HOME ever leaked, that spec would edit the developer's real Codex config
# and the daemon would point their real editors at a throwaway loopback port.
#
# So the harness asserts the machine it is about to hand over. A fresh isolated
# HOME has NO tool config directory in it; this laptop has four. Any row
# reporting `installed` means the daemon is reading a home nobody isolated, and
# the run stops HERE — before the suite starts, not after a spec has written.
INTEGRATIONS_CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "x-keld-agent-secret: $SECRET" "$BASE/v1/integrations")
INTEGRATIONS_MOUNTED=False
if [ "$INTEGRATIONS_CODE" = "200" ]; then
  INTEGRATIONS_MOUNTED=True
  INSTALLED=$(curl -s -H "x-keld-agent-secret: $SECRET" "$BASE/v1/integrations" | python3 -c '
import json,sys
try: rows = json.load(sys.stdin).get("integrations") or []
except Exception: rows = []
print(",".join(r.get("id","?") for r in rows if r.get("installed")))' 2>/dev/null)
  [ -z "$INSTALLED" ] || fail "HOME IS NOT ISOLATED. The daemon reports these tools installed under HOME=$HOME_DIR: $INSTALLED. A fresh isolated home contains none of them, so the daemon is reading a real home and ui/e2e/integrations.spec.ts would rewrite that machine's tool configs. Nothing was changed; fix the isolation before re-running."
  echo "e2e-up: integrations route mounted; no tool installed under the isolated HOME (isolation holds)"
else
  echo "e2e-up: WARNING integrations route answered $INTEGRATIONS_CODE — the live integrations journeys will skip"
fi

python3 - "$WORK/state.json" <<PY
import json,sys,os
json.dump({
  "baseURL": "$BASE",
  "secret": "$SECRET",
  "port": $PORT,
  "daemonPid": $DAEMON_PID,
  "pgid": os.getpgid($DAEMON_PID),
  "sidecarPidFile": "$WORK/sidecar.pid",
  "home": "$HOME_DIR",
  "work": "$WORK",
  "repoRoot": "$ROOT",
  "log": "$LOG",
  "settingsMounted": $SETTINGS_MOUNTED,
  "integrationsMounted": $INTEGRATIONS_MOUNTED,
  "blocks": $N,
  "intended": $INTENDED,
}, open(sys.argv[1], "w"), indent=1)
PY
[ -s "$WORK/state.json" ] || fail "could not write state.json"
echo "e2e-up: ready (settings route: $SETTINGS_CODE, integrations route: $INTEGRATIONS_CODE) -> $WORK/state.json"
