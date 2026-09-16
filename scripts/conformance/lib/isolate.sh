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
  echo "conformance: FAIL [seed ${SEED:-?} chain ${CHAIN:-?} step ${STEP:-?}]: $*" >&2
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

# --- binaries and the sidecar they talk to -----------------------------------
#
# Chain B swaps both halves of the install mid-run, so "which keld" and "which
# sidecar" are variables rather than constants. Both are also what makes the
# version-skew assertion mean anything: a build with no version reports `dev`,
# and `version.Skew` answers `known=false` for a dev half — correctly, since a
# source checkout cannot be compared to anything. So the harness STAMPS the
# binaries it builds, or its skew check would be vacuous on every developer
# machine, which is the shape of the outage it exists to catch.

# keld_build <dir> <version> — build keld + keld-agent, stamped.
keld_build() {
  local dir=$1 ver=$2
  mkdir -p "$dir"
  local ldflags="-X github.com/ncx-ai/keld-signal/internal/version.CLI=$ver"
  (cd "$ROOT" && go build -ldflags "$ldflags" -o "$dir/keld" ./cmd/keld) || fail "go build ./cmd/keld"
  (cd "$ROOT" && go build -ldflags "$ldflags" -o "$dir/keld-agent" ./cmd/keld-agent) || fail "go build ./cmd/keld-agent"
}

# bin_use <dir> — the keld/keld-agent this run drives from now on.
bin_use() {
  BIN_DIR=$1
  KELD_VERSION_SEEN=$("$BIN_DIR/keld-agent" --version 2>/dev/null | head -1)
  say "keld binaries: $BIN_DIR ($KELD_VERSION_SEEN)"
}

# sidecar_point_at <worktree|/path/to/frozen/dir> — (re)write the wrapper the
# daemon spawns. Called again by chain B's upgrade step, which is the only way
# to replace one half of an install while the other stays put.
sidecar_point_at() {
  local what=$1
  if [ "$what" = "worktree" ]; then
    # ⚠️ **A MISSING $PY USED TO PRODUCE A WRAPPER THAT COULD NOT RUN, AND SAID
    # NOTHING.** The wrapper was written unconditionally, the daemon exec'd a
    # python that is not there, the sidecar never started, and the run failed
    # three checkpoints later with `store_rows 0 prompt row(s)` — a sentence
    # that points at the store, which was fine, instead of at the interpreter,
    # which was absent.
    #
    # Measured on GitHub 2026-09-16: chain B on ubuntu and macOS both failed
    # exactly that way. A runner has no ~/.keld/sidecar-venv — `make sidecar`
    # is a developer step — so "upgrade to the worktree sidecar" is not a thing
    # CI can do, and the harness reported it as a product failure.
    #
    # Fail HERE, naming the interpreter. A harness that cannot tell you which
    # half is missing is worse than one that refuses to start.
    [ -x "$PY" ] || fail "sidecar_point_at worktree: no python at $PY.
  The worktree sidecar runs serve.py under the venv 'make sidecar' creates, and
  there is none here. Set KELD_CONFORM_PYTHON, or point this run at a frozen
  sidecar with KELD_CONFORM_NEW_SIDECAR=<dir> (CI does the latter: a runner
  never has the venv)."
    SIDECAR_TARGET="\"$PY\" \"$ROOT/sidecar/serve.py\""
    SIDECAR_KIND="venv (worktree serve.py, $PY)"
    SIDECAR_VERSION_SEEN="dev"
  else
    local sc_bin=keld-agent-sidecar
    case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) sc_bin=keld-agent-sidecar.exe ;; esac
    SIDECAR_TARGET="\"$what/$sc_bin\""
    SIDECAR_VERSION_SEEN=$(cat "$what/VERSION" 2>/dev/null || echo "dev")
    SIDECAR_KIND="frozen $SIDECAR_VERSION_SEEN at $what"
  fi
  # ⚠️ **WINDOWS CANNOT EXECUTE THE WRAPPER, SO IT DOES NOT GET ONE.** The
  # wrapper is a `#!/bin/sh` script and the thing that launches it is the Go
  # daemon — a Windows binary, which has no shebang handling. Pointing
  # KELD_SIDECAR_BIN at it there gives a sidecar that never starts, and the
  # run fails three checkpoints later reading an empty store rather than at the
  # launch that did not happen.
  #
  # Nothing is lost by skipping it. The wrapper exists ONLY to record the
  # process GROUP for teardown, and process groups are a POSIX concept: the
  # daemon's own Windows reaping is `taskkill /T` over the process tree
  # (procgroup_windows.go), which needs no pgid file. So on Windows the daemon
  # is pointed straight at the executable.
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
      # SIDECAR_TARGET carries its own quoting for the POSIX `exec` line; the
      # env var needs the bare path.
      SIDECAR_LAUNCH=$(printf '%s' "$SIDECAR_TARGET" | tr -d '"')
      [ -f "$SIDECAR_LAUNCH" ] || fail "no sidecar executable at $SIDECAR_LAUNCH"
      return 0
      ;;
  esac
  cat > "$WORK/sidecar-wrapper" <<SH
#!/bin/sh
# Record the process GROUP so teardown can reap the sidecar's children even if
# the daemon is already gone.
ps -o pgid= -p \$\$ | tr -d ' ' > "$SIDECAR_PID_FILE"
exec $SIDECAR_TARGET "\$@"
SH
  chmod +x "$WORK/sidecar-wrapper"
  SIDECAR_LAUNCH="$WORK/sidecar-wrapper"
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
    # ⚠️ Windows: the binary carries .exe, and the Inno installer lays the
    # sidecar FLAT into {localappdata}\Programs\keld (its [Files] line is
    # `Source: "keld-agent-sidecar\*"; DestDir: "{app}"`), not into a
    # keld-agent-sidecar/ subdirectory like the tarballs do. Both shapes are
    # searched rather than assumed, because resolveSidecar in the daemon
    # accepts both too.
    local sc_bin=keld-agent-sidecar
    case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) sc_bin=keld-agent-sidecar.exe ;; esac
    local win_app=""
    [ -n "${LOCALAPPDATA:-}" ] && win_app=$(cygpath -u "$LOCALAPPDATA" 2>/dev/null)/Programs/keld
    for d in "$HOME/.local/bin/keld-agent-sidecar" "/usr/local/keld/keld-agent-sidecar" ${win_app:+"$win_app"}; do
      if [ -x "$d/$sc_bin" ] || [ -f "$d/$sc_bin" ]; then
        # ⚠️ The DIRECTORY, not the executable inside it. `sidecar_point_at`
        # takes a tree (it reads VERSION beside the binary), and handing it the
        # executable produced a wrapper that exec'd
        # …/keld-agent-sidecar/keld-agent-sidecar/keld-agent-sidecar. The sidecar
        # then never started and three checkpoints failed at once with no line
        # anywhere saying why — caught by running the reference chain, not by
        # reading it.
        FROZEN_SIDECAR="$d"
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
  (cd "$ROOT" && go build -o "$WORK/keld-conform" ./cmd/keld-conform) || fail "go build ./cmd/keld-conform"
  keld_build "$WORK/bin-under-test" "$UNDER_TEST_VERSION"
  bin_use "$WORK/bin-under-test"

  # What a fresh install lands on (settings.WriteInstallDefaults): the
  # model-free facet set plus the block emitter. Under the compiled-in "auto"
  # default the first enrichment job downloads ~1.9 GB of GLiNER2 weights — the
  # one network access a conformance run must never make.
  cat > "$ISO_HOME/agent-config.json" <<JSON
{"ml_backend":"deterministic","blocks":true,"attribution":false}
JSON

  if [ -n "${FROZEN_SIDECAR:-}" ]; then
    sidecar_point_at "$FROZEN_SIDECAR"
  else
    sidecar_point_at worktree
  fi

  TELEMETRY_PORT=$(free_port)

  export HOME="$ISO_HOME"
  export KELD_HOME="$ISO_HOME"
  export KELD_TELEMETRY_PORT="$TELEMETRY_PORT"
  # The wrapper on POSIX, the executable itself on Windows — see sidecar_point_at.
  export KELD_SIDECAR_BIN="${SIDECAR_LAUNCH:-$WORK/sidecar-wrapper}"
  export KELD_WATCH_POLL=2s
  export KELD_BLOCKS=1
  export KELD_BLOCKS_INTERVAL=20s
  export KELD_BLOCKS_BACKFILL=1
  # named_terms loads spaCy (~619 MB) into a parent that is never recycled, and
  # no checkpoint reads it.
  export KELD_TERMS=0
  # The skew event and every other operational event reach the mock through the
  # reporter's batch flush (30s by default), and a chain step that waited that
  # long per assertion would spend its budget on a timer.
  export KELD_CLIENTEVENTS_FLUSH=5s
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

# artifact_install — AC-12's half of the Signal-install step: install a REAL
# release artifact the unattended way, onboard it by command, and verify the
# machine from observed state. A no-op unless the run was given `--artifact
# dir:<path>`.
#
# ⚠️ **IT RUNS AGAINST THE MACHINE'S OWN HOME, NOT THE ISOLATED ONE, AND THAT
# IS THE POINT.** A package installs system-wide and registers a service;
# neither is isolated by KELD_HOME (AGENTS.md: service.Install resolves the
# service path from os.UserHomeDir()). Pretending otherwise would verify a
# LaunchAgent nothing would ever load. So this step proves the INSTALLER on the
# machine, and the chain's five lanes go on being proved in the isolated HOME
# immediately afterwards by signal_install — the split is deliberate and is why
# the chain's shape does not change.
#
# ⚠️ **THE DISPOSABLE GUARD IS NOT SET HERE.** install-<os>.sh refuses unless
# the machine says it is disposable, and a harness that quietly set that
# variable would have deleted the guard rather than passed it. The VM, the
# container and CI each set it themselves.
artifact_install() {
  case "${ARTIFACT:-none}" in
    none|"") return 0 ;;
    dir:*)   ARTIFACT_DIR=${ARTIFACT#dir:} ;;
    *) say "--artifact must be 'none' or 'dir:<path>' (got $ARTIFACT)"; return 1 ;;
  esac
  [ -d "$ARTIFACT_DIR" ] || { say "no such artifacts directory: $ARTIFACT_DIR"; return 1; }
  ARTIFACT_DIR=$(cd "$ARTIFACT_DIR" && pwd)

  local script
  case "$(uname -s)" in
    Darwin) script="$ROOT/scripts/conformance/install-macos.sh" ;;
    Linux)  script="$ROOT/scripts/conformance/install-linux.sh" ;;
    *) say "no unattended installer script for $(uname -s); Windows runs install-windows.ps1 directly"; return 1 ;;
  esac

  if [ "${CI:-}" != "true" ] && [ "${KELD_CONFORM_DISPOSABLE:-0}" != "1" ]; then
    say "--artifact installs a REAL release on THIS machine (service registration included)."
    say "  Set KELD_CONFORM_DISPOSABLE=1 only where the machine is disposable — a VM, a"
    say "  container, or CI. Refusing rather than rewriting a developer's own service."
    return 1
  fi

  local out="$WORK/artifact-install.log"
  say "installing the release under test from $ARTIFACT_DIR (unattended, AC-12)"
  # KELD_CONFORM_INSTALL_FLAGS is how an environment states what it cannot
  # prove — a GitHub runner has no systemd --user bus, so the Linux leg passes
  # --allow-no-service-manager. It narrows ONE check to the unit file and says
  # so in the output; it is not a way to turn verification off.
  #
  # `--stop-service` leaves the job REGISTERED (so what was verified stays true)
  # but not running: the chain drives its own foreground daemon and must not
  # race a launchd/systemd one over the same ports.
  env HOME="$REAL_HOME" KELD_HOME="$REAL_HOME/.keld" \
      bash "$script" --artifacts "$ARTIFACT_DIR" --code CONFORM \
        --api-url "$MOCK_ATLAS_URL" --stop-service \
        ${KELD_CONFORM_INSTALL_FLAGS:-} 2>&1 | tee "$out"
  # ⚠️ PIPESTATUS must be read on the very next COMMAND; only comments may sit
  # between (they are not commands and do not reset it).
  [ "${PIPESTATUS[0]}" = "0" ] || { say "the unattended install FAILED — see $out"; return 1; }

  # The installed tree is what the rest of the chain drives, so a green chain is
  # a statement about the ARTIFACT rather than about a source build.
  local bin sc
  bin=$(sed -n 's/^install-[a-z]*: bin_dir=//p' "$out" | tail -1)
  sc=$(sed -n 's/^install-[a-z]*: sidecar_dir=//p' "$out" | tail -1)
  [ -n "$bin" ] && [ -x "$bin/keld-agent" ] \
    || { say "the installer reported no usable bin_dir (got '${bin:-}')"; return 1; }
  bin_use "$bin"
  if [ -n "$sc" ] && [ -x "$sc/keld-agent-sidecar" ]; then
    sidecar_point_at "$sc"
    say "chain now runs the INSTALLED halves: $bin + $SIDECAR_KIND"
  else
    say "the installed sidecar is not on disk yet ($sc); keeping $SIDECAR_KIND"
    say "  — on macOS postinstall fetches it in the background, so this is timing, not failure"
  fi
  return 0
}

# signal_install — onboarding as a person would do it, through the commands the
# installers call: a setup code, then tool configuration. `keld-agent install`
# is deliberately NOT called (see the header).
signal_install() {
  say "keld login --code (against the mock Atlas)"
  "$BIN_DIR/keld" login --code CONFORM --api-url "$MOCK_ATLAS_URL" >"$WORK/login.out" 2>&1 \
    || fail "keld login failed: $(cat "$WORK/login.out")"

  say "keld signal setup --yes"
  "$BIN_DIR/keld" signal setup --yes >"$WORK/setup.out" 2>&1 \
    || fail "keld signal setup failed: $(tail -20 "$WORK/setup.out")"

  [ -s "$ISO_HOME/hook.json" ] || fail "setup wrote no hook.json"
  grep -q ingest_token "$ISO_HOME/hook.json" || fail "hook.json carries no ingest token"
}

# agent_json_field <key> — one field of ~/.keld/agent.json, empty when absent.
agent_json_field() {
  python3 -c "import json,sys
try: print(json.load(open(sys.argv[1])).get(sys.argv[2],''))
except Exception: print('')" "$ISO_HOME/agent.json" "$1" 2>/dev/null || echo ""
}

# daemon_start — foreground daemon + worktree sidecar; waits for agent.json.
daemon_start() {
  # ⚠️ READ FIRST, LAUNCH SECOND. The daemon writes agent.json within
  # milliseconds of exec, so reading the previous ingress secret after starting
  # it is a race the harness loses: it reads the NEW secret as the OLD one and
  # then waits out the whole 90s budget for a change that already happened.
  local prev_secret=""
  [ -f "$ISO_HOME/agent.json" ] && prev_secret=$(agent_json_field secret)

  say "starting keld-agent (foreground): $BIN_DIR/keld-agent $KELD_VERSION_SEEN against $SIDECAR_KIND"
  "$BIN_DIR/keld-agent" run >>"$DAEMON_LOG" 2>&1 &
  DAEMON_PID=$!

  # ⚠️ **NEVER DELETE agent.json TO WAIT FOR A RESTART.** It carries
  # `telemetry_secret` — the STABLE local secret `keld signal setup` wrote into
  # every tool's config — beside the per-start ingress secret. Removing it makes
  # the next daemon mint a new one, and every already-configured tool then posts
  # a credential the proxy rejects. Measured when this function did exactly that
  # for one commit: claude posted OTLP, the proxy answered 401, nothing was
  # spooled, and the telemetry checkpoint read a flat zero — which is the
  # rotated-credential outage AGENTS.md describes, rebuilt by the harness meant
  # to catch it. The restart is detected by the INGRESS secret changing instead:
  # the daemon regenerates that one on every start, by design.
  local i now_secret
  for i in $(seq 1 90); do
    if [ -f "$ISO_HOME/agent.json" ]; then
      now_secret=$(agent_json_field secret)
      [ -n "$now_secret" ] && [ "$now_secret" != "$prev_secret" ] && break
    fi
    kill -0 "$DAEMON_PID" 2>/dev/null || fail "daemon exited early"
    sleep 1
  done
  [ -f "$ISO_HOME/agent.json" ] || fail "no agent.json after 90s"

  # ⚠️ agent.json appears BEFORE its port is filled in, so waiting on the file
  # alone reported "daemon on http://127.0.0.1:0". Wait for the port itself.
  for i in $(seq 1 30); do
    DAEMON_PORT=$(agent_json_field port)
    [ "${DAEMON_PORT:-0}" -gt 0 ] && break
    sleep 1
  done
  [ "${DAEMON_PORT:-0}" -gt 0 ] || fail "agent.json never carried a port"
  DAEMON_SECRET=$(agent_json_field secret)
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

# daemon_stop — stop the daemon and its sidecar's whole process group, and WAIT.
# Chain B restarts a second daemon on the same isolated HOME, so a straggler
# holding the loopback port or the store's WAL is not a tidiness question.
daemon_stop() {
  [ -n "${DAEMON_PID:-}" ] || return 0
  kill -TERM "$DAEMON_PID" 2>/dev/null || true
  local i
  for i in $(seq 1 20); do
    kill -0 "$DAEMON_PID" 2>/dev/null || break
    sleep 1
  done
  kill -KILL "$DAEMON_PID" 2>/dev/null || true
  if [ -f "$SIDECAR_PID_FILE" ]; then
    local pgid
    pgid=$(cat "$SIDECAR_PID_FILE" 2>/dev/null || echo "")
    [ -n "$pgid" ] && kill -KILL "-$pgid" 2>/dev/null || true
    rm -f "$SIDECAR_PID_FILE"
  fi
  DAEMON_PID=""
  say "daemon stopped"
}

# clientevent_count <code> — how many times an operational event reached the
# mock Atlas. Read from the PERSISTED BODIES rather than from a log line,
# because the fleet's view of a machine is what arrived at Atlas; a log line the
# reporter never flushed is exactly the invisible state this chain is about.
clientevent_count() {
  local code=$1 dir="$ATLAS_STATE/v1_signal_client-events"
  [ -d "$dir" ] || { echo 0; return 0; }
  grep -ho "\"$code\"" "$dir"/* 2>/dev/null | wc -l | tr -d ' '
}

# await_clientevent <code> <seconds> — wait for one to arrive. The reporter
# batches and flushes on KELD_CLIENTEVENTS_FLUSH, which isolate_init shortens.
await_clientevent() {
  local code=$1 budget=${2:-30} i
  for i in $(seq 1 "$budget"); do
    [ "$(clientevent_count "$code")" -gt 0 ] && return 0
    sleep 1
  done
  return 1
}
