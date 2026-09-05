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
#    "home","work","log","settingsMounted","blocks"}
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
cat > "$HOME_DIR/agent-config.json" <<JSON
{"ml_backend":"deterministic","blocks":true}
JSON

# One workstream so the Projects pane has a "counts for my work" switch to
# drive and a bucket for "New project" to land in. This is the org-vocabulary
# shape docs/v3/contracts.md defines; no projects yet, so every repository
# starts out as a suggestion.
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
export KELD_WATCH_BACKFILL=${KELD_WATCH_BACKFILL:-0}
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
echo "e2e-up: first block landed after $((SECONDS - (DEADLINE - READY_TIMEOUT)))s; settling until $INTENDED are cut (up to ${SETTLE_TIMEOUT}s)"
DEADLINE=$((SECONDS + SETTLE_TIMEOUT))
while [ $SECONDS -lt $DEADLINE ] && [ "$N" -lt "$INTENDED" ]; do
  sleep 3
  N=$(curl -s -H "x-keld-agent-secret: $SECRET" "$BASE/v1/ledger" | python3 -c '
import json,sys
try: print(len(json.load(sys.stdin).get("blocks") or []))
except Exception: print(0)' 2>/dev/null)
done
echo "e2e-up: $N of $INTENDED intended blocks in the ledger"

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
  "log": "$LOG",
  "settingsMounted": $SETTINGS_MOUNTED,
  "blocks": $N,
  "intended": $INTENDED,
}, open(sys.argv[1], "w"), indent=1)
PY
[ -s "$WORK/state.json" ] || fail "could not write state.json"
echo "e2e-up: ready (settings route: $SETTINGS_CODE) -> $WORK/state.json"
