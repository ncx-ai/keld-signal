#!/usr/bin/env bash
# Shared by scripts/conformance/install-macos.sh and install-linux.sh: the
# disposable-machine guard, the step framing every failure line is named after,
# and the three verifications AC-12 asks for.
#
# ⚠️ **EVERY VERIFICATION READS OBSERVED STATE, NEVER AN EXIT CODE.** This is
# the whole point of the criterion and it is a lesson this repo has already
# paid for twice: `keld-agent install` can exit 0 having only registered the
# scheduled task, and the Windows `[Run]` step exited 0 while every machine
# idled on `awaitConfig` forever. `onboard.cmd` already states the rule — "claim
# success only if it is true: setup is done when an ingest token exists in
# hook.json" — and these scripts are the same rule applied to all three
# installers.
#
# So: the binaries are checked by RUNNING them, the service by asking the
# platform's own service manager, and onboarding by reading the file the daemon
# itself reads.

# --- step framing ------------------------------------------------------------
#
# IV_STEP is the name every failure line carries. A failure that does not name
# its step is unactionable: "the installer failed" says nothing about whether
# the package refused to install, the login was rejected, or the service never
# registered.

IV_NAME=${IV_NAME:-install}
IV_STEP="start"

iv_say()  { echo "$IV_NAME: $*"; }
iv_step() { IV_STEP=$1; echo "$IV_NAME: --- step $IV_STEP ---"; }
iv_ok()   { echo "$IV_NAME:   ✓ $*"; }

# iv_die <message...> — fail loudly, naming the step.
iv_die() {
  echo "" >&2
  echo "$IV_NAME: FAILED at step $IV_STEP: $*" >&2
  echo "$IV_NAME: the machine is NOT installed and onboarded; nothing downstream may assume it is." >&2
  exit 1
}

# iv_run <step> <command...> — run a command, failing with its own output.
# The exit code decides whether the COMMAND ran, never whether the machine
# ended up in the wanted state; that is what the verifications below are for.
iv_run() {
  local step=$1; shift
  iv_step "$step"
  iv_say "\$ $*"
  local out rc
  out=$("$@" 2>&1); rc=$?
  [ -n "$out" ] && printf '%s\n' "$out" | sed "s/^/$IV_NAME:   | /"
  return $rc
}

# --- the guard ---------------------------------------------------------------
#
# ⚠️ **THESE SCRIPTS REWRITE THE MACHINE THEY RUN ON**: they install a package
# system-wide, register a real LaunchAgent / systemd unit / scheduled task, and
# let `keld signal setup` rewrite the AI tools' own config files. AGENTS.md says
# it plainly for the one case a developer hits first — "do not run
# `keld-agent install` to test the config write: KELD_HOME isolates ~/.keld but
# NOT the service path, which service.Install resolves from os.UserHomeDir() —
# it will rewrite your real unit to point at the `go run` temp binary and
# restart it into `failed`."
#
# So the machine must SAY it is disposable. Not inferred from a hostname or the
# absence of a $HOME/dev directory: a guess that is wrong once costs somebody
# their working install, and the environments that legitimately run this (CI,
# a Tart VM, a container) can all set one variable.
iv_require_disposable() {
  if [ "${CI:-}" = "true" ] || [ "${KELD_CONFORM_DISPOSABLE:-0}" = "1" ]; then
    return 0
  fi
  # ⚠️ **QUOTED HEREDOC, AND THAT IS NOT A STYLE CHOICE.** This text names shell
  # commands. An UNQUOTED delimiter makes the shell expand the body, so a
  # backticked example is EXECUTED while the warning is being printed — measured
  # on 2026-09-16 while writing this file: the refusal message ran a real
  # `keld signal setup` on the author's own machine and hung on its confirmation
  # prompt. A guard that runs the command it is refusing to run is worse than no
  # guard. Nothing dynamic goes in here; the invocation is echoed separately.
  cat >&2 <<'MSG'

REFUSING TO RUN — this machine has not been declared disposable.

  These scripts install Signal for real: a system package, a registered
  LaunchAgent / systemd unit / scheduled task, and "keld signal setup"
  rewriting your AI tools' own config files.

  AGENTS.md states the sharpest edge: KELD_HOME isolates ~/.keld but NOT the
  service path, which service.Install resolves from os.UserHomeDir() — so on a
  developer machine this rewrites the service you actually use and restarts it
  into "failed".

  Run it where the machine is disposable: a Tart VM
  (scripts/conformance/tart/), the Linux container
  (scripts/conformance/compose/), or CI. To override deliberately, set:

      KELD_CONFORM_DISPOSABLE=1

MSG
  echo "  (refused: $0)" >&2
  exit 2
}

# --- reading observed state --------------------------------------------------

iv_keld_home() { echo "${KELD_HOME:-$HOME/.keld}"; }

# iv_json_string <file> <key> — one top-level string field, empty when absent.
# python3 where there is one (the sidecar needs it anyway), grep as the fallback
# so a minimal image still verifies rather than silently passing.
iv_json_string() {
  local file=$1 key=$2
  [ -f "$file" ] || { echo ""; return 0; }
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json,sys
try:
    v = json.load(open(sys.argv[1])).get(sys.argv[2], "")
    print(v if isinstance(v, str) else "")
except Exception:
    print("")' "$file" "$key"
    return 0
  fi
  sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$file" | head -1
}

# iv_verify_binaries <dir> — `keld` and `keld-agent` exist there AND RUN.
#
# Running them is the point: a wrong-architecture build hashes correctly and
# cannot execute, which is why update/ runs `keld-agent --version` on a staged
# binary before any swap. A `test -x` would pass on exactly that binary.
iv_verify_binaries() {
  local dir=$1 b path ver
  for b in keld keld-agent; do
    path="$dir/$b"
    [ -x "$path" ] || iv_die "no executable $path — the installer placed no $b"
    ver=$("$path" --version 2>&1 | head -1) \
      || iv_die "$path exists but would not run: $ver"
    [ -n "$ver" ] || iv_die "$path ran but reported no version"
    iv_ok "$path — $ver"
  done
}

# iv_verify_onboarded — hook.json holds a non-empty ingest token.
#
# THE one fact that says this machine will collect anything. `keld signal setup`
# exiting 0 does not: the daemon reads this file, and an empty or absent one is
# the documented idle state (`awaitConfig`), which is exactly what every Windows
# machine sat in while its installer reported success.
iv_verify_onboarded() {
  local home hook token
  home=$(iv_keld_home)
  hook="$home/hook.json"
  [ -f "$hook" ] || iv_die "no $hook — onboarding never wrote one, so the daemon will idle on awaitConfig forever"
  token=$(iv_json_string "$hook" ingest_token)
  [ -n "$token" ] || iv_die "$hook carries no ingest_token — the machine is installed but NOT set up, and nothing is being collected"
  iv_ok "$hook holds an ingest token (${#token} chars, not printed)"
}

# iv_verify_service_darwin — the LaunchAgent is on disk AND known to launchd.
#
# Both halves: the plist alone proves a file was written, and `launchctl print`
# alone cannot be read on a machine where the job was registered for a different
# user. Together they are what "the service is registered" means.
iv_verify_service_darwin() {
  local plist="$HOME/Library/LaunchAgents/co.keld.agent.plist" out
  [ -f "$plist" ] || iv_die "no $plist — nothing registered the LaunchAgent"
  grep -q "keld-agent" "$plist" || iv_die "$plist does not name keld-agent"
  iv_ok "$plist exists and names keld-agent"
  if out=$(launchctl print "gui/$(id -u)/co.keld.agent" 2>&1); then
    iv_ok "launchctl knows gui/$(id -u)/co.keld.agent ($(printf '%s' "$out" | sed -n 's/.*state = \([a-z]*\).*/\1/p' | head -1))"
  else
    iv_die "launchctl does not know gui/$(id -u)/co.keld.agent: $(printf '%s' "$out" | head -2)"
  fi
}

# iv_verify_service_linux [--allow-no-manager]
#
# ⚠️ A CONTAINER HAS NO SYSTEMD USER BUS, and that is a property of the
# environment rather than of the installer. The unit FILE is still observable
# and is still required; only the "is it loaded" half is downgraded, and only
# when the caller passed the flag — so a missing bus can never quietly turn
# this whole check into a pass.
iv_verify_service_linux() {
  local allow=${1:-}
  local unit="$HOME/.config/systemd/user/keld-agent.service" state
  [ -f "$unit" ] || iv_die "no $unit — nothing registered the systemd user service"
  grep -q "keld-agent" "$unit" || iv_die "$unit does not name keld-agent"
  iv_ok "$unit exists and names keld-agent"

  if ! command -v systemctl >/dev/null 2>&1; then
    [ "$allow" = "--allow-no-manager" ] \
      || iv_die "no systemctl on this machine; pass --allow-no-service-manager to install-linux.sh if that is expected here"
    iv_say "  ⚠ no systemctl: the unit file is verified, whether it LOADS is not (stated, not assumed)"
    return 0
  fi
  state=$(systemctl --user is-enabled keld-agent.service 2>&1)
  case "$state" in
    enabled|enabled-runtime|static|linked*)
      iv_ok "systemctl --user is-enabled keld-agent.service -> $state"
      iv_say "  systemctl --user is-active  -> $(systemctl --user is-active keld-agent.service 2>&1)"
      ;;
    *)
      if [ "$allow" = "--allow-no-manager" ]; then
        iv_say "  ⚠ systemctl answered '$state' (no user bus here): the unit file is verified, whether it LOADS is not"
      else
        iv_die "systemctl --user is-enabled keld-agent.service answered '$state'"
      fi
      ;;
  esac
}
