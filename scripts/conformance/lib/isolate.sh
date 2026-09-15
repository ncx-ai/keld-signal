#!/usr/bin/env bash
# Isolation for the conformance harness.
#
# ⚠️ **HOME and KELD_HOME are BOTH overridden**, exactly as scripts/e2e-up.sh
# does and for the same reason: KELD_WATCH_ROOTS only ADDS a root, so leaving
# HOME alone would point the watcher at the developer's own transcripts and
# publish their sessions to the mock. Every per-tool config dir is under that
# HOME too, so the real ~/.claude, ~/.codex and ~/.keld are never read or
# written by a conformance run.
#
# ⚠️ **The daemon runs FOREGROUND; `keld-agent install` is never called.**
# KELD_HOME isolates ~/.keld but NOT the service path, which service.Install
# resolves from os.UserHomeDir() — running it here would rewrite the developer's
# real LaunchAgent to point at this run's temp binary. The installer path is
# AC-12's business and belongs to the VM/container legs (task A.4), where the
# machine is disposable.

# --- failure reporting -------------------------------------------------------

fail() {
  echo "conformance: FAIL: $*" >&2
  if [ -n "${DAEMON_LOG:-}" ] && [ -f "$DAEMON_LOG" ]; then
    echo "--- last 40 lines of the daemon log ---" >&2
    tail -40 "$DAEMON_LOG" >&2
  fi
  teardown
  exit 1
}

say() { echo "conformance: $*"; }

# --- lifecycle ---------------------------------------------------------------

teardown() {
  # Kill the daemon's whole process GROUP: the supervisor gives the sidecar one
  # via Setpgid, and killing the bare pid leaves a multi-hundred-MB Python child
  # reparented to init. Same reasoning as procgroup_*.go.
  if [ -n "${DAEMON_PID:-}" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill -TERM "$DAEMON_PID" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      kill -0 "$DAEMON_PID" 2>/dev/null || break
      sleep 1
    done
    kill -KILL "$DAEMON_PID" 2>/dev/null || true
  fi
  if [ -n "${SIDECAR_PID_FILE:-}" ] && [ -f "$SIDECAR_PID_FILE" ]; then
    local pgid
    pgid=$(cat "$SIDECAR_PID_FILE" 2>/dev/null || echo "")
    [ -n "$pgid" ] && kill -KILL "-$pgid" 2>/dev/null || true
  fi
  for pid in ${MOCK_PIDS:-}; do kill -TERM "$pid" 2>/dev/null || true; done
}

free_port() {
  python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'
}

# isolate_init <workdir> — build the binaries and lay out the isolated HOME.
isolate_init() {
  WORK=$1
  ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
  mkdir -p "$WORK"
  WORK=$(cd "$WORK" && pwd)

  ISO_HOME="$WORK/home"
  DAEMON_LOG="$WORK/daemon.log"
  SIDECAR_PID_FILE="$WORK/sidecar.pgid"
  ATLAS_STATE="$WORK/atlas-state"
  MOCK_PIDS=""

  command -v go >/dev/null || fail "go toolchain not on PATH"
  command -v python3 >/dev/null || fail "python3 not on PATH"

  # The analysis sidecar, resolved in this order and REPORTED either way,
  # because which one ran changes what a green result means:
  #
  #  1. the venv `make sidecar` creates, running the WORKTREE's serve.py — the
  #     same call e2e-up.sh makes, and what a developer changing sidecar code
  #     needs a conformance run to exercise;
  #  2. the FROZEN sidecar an installer put on this machine, which is what a
  #     user actually runs. It may be older than the worktree — that skew is a
  #     real property of the machine and is named in the output rather than
  #     avoided, since hiding it is exactly the ~3-week blocks outage.
  PY=${KELD_CONFORM_PYTHON:-$HOME/.keld/sidecar-venv/bin/python}
  SIDECAR_KIND=""
  if [ -x "$PY" ]; then
    SIDECAR_KIND="venv (worktree serve.py, $PY)"
  else
    for d in "$HOME/.local/bin/keld-agent-sidecar" "/usr/local/keld/keld-agent-sidecar"; do
      if [ -x "$d/keld-agent-sidecar" ]; then
        FROZEN_SIDECAR="$d/keld-agent-sidecar"
        SIDECAR_KIND="frozen $(cat "$d/VERSION" 2>/dev/null || echo "(no VERSION — predates the stamp)") at $d"
        break
      fi
    done
  fi
  [ -n "$SIDECAR_KIND" ] || fail "no analysis sidecar: run 'make sidecar' for the venv, install one, or set KELD_CONFORM_PYTHON"
  say "sidecar: $SIDECAR_KIND"
  if [ -n "${FROZEN_SIDECAR:-}" ]; then
    # ⚠️ A frozen sidecar is a RELEASED artifact and this worktree is not. Any
    # lane whose sidecar half is new here — the Codex reader is the live
    # example, and `store_rows` is the checkpoint that reads it — is being
    # asked of code that predates the change, so a failure means "that release
    # cannot do this yet", not "this branch is broken". Saying which one ran is
    # the whole lesson of the three-week sidecar skew; saying it only in
    # passing is how that outage stayed invisible.
    say "⚠️  that sidecar is a RELEASE, not this worktree. A checkpoint that depends"
    say "    on a sidecar change made here CANNOT pass on it. Point the run at the"
    say "    worktree with KELD_CONFORM_PYTHON=<python with sidecar/requirements.txt>."
  fi

  rm -rf "$ISO_HOME" "$ATLAS_STATE" "$WORK/state.json"
  mkdir -p "$ISO_HOME/state" "$ATLAS_STATE"

  say "building keld, keld-agent, keld-conform"
  (cd "$ROOT" && go build -o "$WORK/keld" ./cmd/keld) || fail "go build ./cmd/keld"
  (cd "$ROOT" && go build -o "$WORK/keld-agent" ./cmd/keld-agent) || fail "go build ./cmd/keld-agent"
  (cd "$ROOT" && go build -o "$WORK/keld-conform" ./cmd/keld-conform) || fail "go build ./cmd/keld-conform"

  # What a fresh install lands on (settings.WriteInstallDefaults): the
  # model-free facet set plus the block emitter. Under the compiled-in "auto"
  # default the first enrichment job downloads ~1.9 GB of GLiNER2 weights — the
  # one network access a conformance run must never make.
  cat > "$ISO_HOME/agent-config.json" <<JSON
{"ml_backend":"deterministic","blocks":true,"attribution":false}
JSON

  if [ -n "${FROZEN_SIDECAR:-}" ]; then
    SIDECAR_TARGET="\"$FROZEN_SIDECAR\""
  else
    SIDECAR_TARGET="\"$PY\" \"$ROOT/sidecar/serve.py\""
  fi
  cat > "$WORK/sidecar-wrapper" <<SH
#!/bin/sh
# Record the process GROUP so teardown can reap the sidecar's children even if
# the daemon is already gone.
ps -o pgid= -p \$\$ | tr -d ' ' > "$SIDECAR_PID_FILE"
exec $SIDECAR_TARGET "\$@"
SH
  chmod +x "$WORK/sidecar-wrapper"

  TELEMETRY_PORT=$(free_port)

  export HOME="$ISO_HOME"
  export KELD_HOME="$ISO_HOME"
  export KELD_TELEMETRY_PORT="$TELEMETRY_PORT"
  export KELD_SIDECAR_BIN="$WORK/sidecar-wrapper"
  export KELD_WATCH_POLL=2s
  export KELD_BLOCKS=1
  export KELD_BLOCKS_INTERVAL=20s
  export KELD_BLOCKS_BACKFILL=1
  # named_terms loads spaCy (~619 MB) into a parent that is never recycled, and
  # no checkpoint reads it.
  export KELD_TERMS=0
  export PATH="$WORK:$PATH"

  say "isolated HOME=$ISO_HOME  telemetry port=$TELEMETRY_PORT"
}

# mocks_start — bring up the mock model and the mock Atlas on loopback.
mocks_start() {
  "$WORK/keld-conform" mockllm --port 0 --log "$WORK/mockllm.jsonl" > "$WORK/mockllm.out" 2>&1 &
  MOCK_PIDS="$MOCK_PIDS $!"
  "$WORK/keld-conform" mockatlas --port 0 --state "$ATLAS_STATE" > "$WORK/mockatlas.out" 2>&1 &
  MOCK_PIDS="$MOCK_PIDS $!"

  MOCK_LLM_URL=$(await_line "$WORK/mockllm.out" 'mockllm listening on \(.*\)')
  [ -n "$MOCK_LLM_URL" ] || fail "mock model did not start: $(cat "$WORK/mockllm.out")"
  MOCK_ATLAS_URL=$(await_line "$WORK/mockatlas.out" 'mockatlas listening on \(.*\)')
  [ -n "$MOCK_ATLAS_URL" ] || fail "mock Atlas did not start: $(cat "$WORK/mockatlas.out")"

  export KELD_API_URL="$MOCK_ATLAS_URL"
  say "mock model $MOCK_LLM_URL  mock Atlas $MOCK_ATLAS_URL"
}

# await_line <file> <sed-capture> — wait up to 10s for a line and echo capture 1.
await_line() {
  local file=$1 pattern=$2 i out
  for i in $(seq 1 100); do
    if [ -f "$file" ]; then
      out=$(sed -n "s|^$pattern.*$|\1|p" "$file" | head -1)
      [ -n "$out" ] && { echo "$out"; return 0; }
    fi
    sleep 0.1
  done
  return 1
}

# signal_install — onboarding as a person would do it, through the commands the
# installers call: a setup code, then tool configuration. `keld-agent install`
# is deliberately NOT called (see the header).
signal_install() {
  say "keld login --code (against the mock Atlas)"
  "$WORK/keld" login --code CONFORM --api-url "$MOCK_ATLAS_URL" >"$WORK/login.out" 2>&1 \
    || fail "keld login failed: $(cat "$WORK/login.out")"

  say "keld signal setup --yes"
  "$WORK/keld" signal setup --yes >"$WORK/setup.out" 2>&1 \
    || fail "keld signal setup failed: $(tail -20 "$WORK/setup.out")"

  [ -s "$ISO_HOME/hook.json" ] || fail "setup wrote no hook.json"
  grep -q ingest_token "$ISO_HOME/hook.json" || fail "hook.json carries no ingest token"
}

# daemon_start — foreground daemon + worktree sidecar; waits for agent.json.
daemon_start() {
  say "starting keld-agent (foreground)"
  "$WORK/keld-agent" run >"$DAEMON_LOG" 2>&1 &
  DAEMON_PID=$!

  local i
  for i in $(seq 1 90); do
    [ -f "$ISO_HOME/agent.json" ] && break
    kill -0 "$DAEMON_PID" 2>/dev/null || fail "daemon exited early"
    sleep 1
  done
  [ -f "$ISO_HOME/agent.json" ] || fail "no agent.json after 90s"

  # ⚠️ agent.json appears BEFORE its port is filled in, so waiting on the file
  # alone reported "daemon on http://127.0.0.1:0". Wait for the port itself.
  for i in $(seq 1 30); do
    DAEMON_PORT=$(python3 -c "import json;print(json.load(open('$ISO_HOME/agent.json')).get('port',0))" 2>/dev/null || echo 0)
    [ "${DAEMON_PORT:-0}" -gt 0 ] && break
    sleep 1
  done
  [ "${DAEMON_PORT:-0}" -gt 0 ] || fail "agent.json never carried a port"
  DAEMON_SECRET=$(python3 -c "import json;print(json.load(open('$ISO_HOME/agent.json'))['secret'])")
  DAEMON_URL="http://127.0.0.1:$DAEMON_PORT"
  say "daemon on $DAEMON_URL"

  # The sidecar is spawned lazily; wait for the store file it creates, since
  # the store_rows checkpoint is meaningless before it exists.
  for i in $(seq 1 60); do
    [ -f "$ISO_HOME/state/refseries.db" ] && break
    kill -0 "$DAEMON_PID" 2>/dev/null || fail "daemon died while the sidecar came up"
    sleep 1
  done
  [ -f "$ISO_HOME/state/refseries.db" ] \
    || say "WARNING: no refseries.db yet; the store_rows checkpoint will say so"
}
