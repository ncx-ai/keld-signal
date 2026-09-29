#!/usr/bin/env bash
# Assertions over postinstall. Every one of these pins a failure that is silent
# on a real machine. Where it matters, the REAL script is RUN with stubbed system
# commands, rather than grepped, so a regression in the script fails here and not
# merely a copy of its logic.
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
p="$d/scripts/postinstall"
code="$(sed 's/#.*//' "$p")"
fail() { echo "FAIL: $1"; exit 1; }

bash -n "$p" || fail "postinstall does not parse"

# ⚠️ -H or the LaunchAgent lands in /var/root/Library/LaunchAgents and never loads:
# service_darwin.go builds the plist path from os.UserHomeDir(), i.e. $HOME.
printf '%s' "$code" | grep -qF 'sudo -u "$user" -H' || fail "user-side commands must run with sudo -H"

# The agent must be registered by postinstall — nothing else does it.
printf '%s' "$code" | grep -qF 'keld-agent install' || fail "postinstall never registers the agent"

# ── INVERTED 2026-09-29: installers only install (web sign-in spec, AC-10). ──
# These four used to REQUIRE the opposite: that postinstall read the wizard
# pane's handoff, ran `signal setup` for the ticked tools, and opened
# onboard.command in Terminal as a fallback. The pane is gone, so there is no
# handoff to read; tools are configured by the daemon's own auto-setup; and the
# fallback asked for a setup code in a Terminal window — the prompt the decision
# removed. Any of them coming back fails here.
case "$code" in *installer-handoff*|*had_handoff*|*HANDOFF*)
  fail "postinstall reads a wizard handoff again; there is no pane to write one" ;; esac
case "$code" in *onboard.command*)
  fail "postinstall opens onboard.command again; no installer prompts for a code" ;; esac
case "$code" in *"signal setup"*)
  fail "postinstall configures tools again; the daemon's auto-setup does that" ;; esac
case "$code" in *"--code"*|*"keld login"*|*"login --code"*)
  fail "postinstall signs in again; Signal asks on first open" ;; esac

# ⚠️ THE FETCH MUST OUTLIVE THIS SCRIPT, AND MUST LEAVE A RECORD. It used to be a
# bare `sh -c "keld signal install-sidecar … >/dev/null 2>&1 &"`. Measured on a
# real v3.0.2 install (2026-09-16): no fetch process, no staging dir, a sidecar
# tree carrying its previous mtime — a bare background child does not survive
# PackageKit tearing down the script's sandbox, and /dev/null meant nothing said so.
grep -q 'launchctl bootstrap' "$p" \
  || fail "the sidecar fetch is not handed to launchd, so it dies with postinstall"
grep -q 'sidecar-install.log' "$p" \
  || fail "the sidecar fetch writes no log, so a silent failure stays silent"
if grep -E 'install-sidecar[^|]*>/dev/null 2>&1 *&' "$p" >/dev/null; then
  fail "the fetch still backgrounds into /dev/null — the exact shape that achieved nothing"
fi

# ── Behaviour: RUN postinstall under stubs, once per install kind. ───────────
# Every system command it calls is replaced by a recorder on PATH; the absolute
# paths it uses (/usr/bin/plutil, /bin/launchctl, …) are rewritten to the stubs.
# $PREFIX and every user path are redirected into a scratch dir, so nothing on
# this machine is touched.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

run_case() { # $1=name  $2=COMMAND_LINE_INSTALL value ("" = unset)  $3=Installer.app running (1/0)
  local c="$work/$1"
  mkdir -p "$c/bin" "$c/prefix" "$c/home/Library/LaunchAgents" "$c/usrlocalbin"
  printf 'v9.9.9\n' > "$c/prefix/VERSION"
  : > "$c/calls"
  for cmd in launchctl sudo open ln stat id dscl chown chmod mkdir pgrep; do
    cat > "$c/bin/$cmd" <<STUB
#!/bin/bash
printf '%s %s\n' "$cmd" "\$*" >> "$c/calls"
case "$cmd" in
  stat) echo 501 ;;
  id) echo tester ;;
  dscl) echo "NFSHomeDirectory: $c/home" ;;
  pgrep) [ "$3" = 1 ] && exit 0 || exit 1 ;;
  mkdir) /bin/mkdir "\$@" 2>/dev/null || true ;;
  launchctl)
    # asuser <uid> <cmd…>: run the remainder so the sudo stub records it.
    if [ "\$1" = asuser ]; then shift 2; "\$@"; fi ;;
  sudo)
    # sudo -u <user> [-H] <cmd…>: strip the options, record the command.
    shift 2; [ "\$1" = -H ] && shift
    printf 'AS_USER %s\n' "\$*" >> "$c/calls"
    case "\$1" in /bin/sh) "\$@" ;; esac ;;
esac
exit 0
STUB
    chmod +x "$c/bin/$cmd"
  done
  sed -e "s#^PREFIX=.*#PREFIX=\"$c/prefix\"#" \
      -e "s#/usr/local/bin#$c/usrlocalbin#g" \
      -e "s#/usr/bin/plutil#plutil#g; s#/bin/launchctl#launchctl#g; s#/usr/bin/pgrep#pgrep#g; s#/usr/bin/open#open#g" \
      -e "s#/Applications/Keld Signal.app#$c/Applications/Keld Signal.app#g" \
      "$p" > "$c/postinstall"
  mkdir -p "$c/Applications/Keld Signal.app"
  ( export PATH="$c/bin:/usr/bin:/bin"
    if [ -n "$2" ]; then export COMMAND_LINE_INSTALL="$2"; else unset COMMAND_LINE_INSTALL; fi
    bash "$c/postinstall" > "$c/out" 2>&1 ) || fail "$1: postinstall exited non-zero: $(cat "$c/out")"
  echo "$c"
}

# A. GUI install: double-clicked pkg, Installer.app running, no COMMAND_LINE_INSTALL.
c="$(run_case gui "" 1)"
grep -q 'AS_USER .*keld-agent install' "$c/calls" || fail "GUI install: the agent is not registered as the console user"
grep -q 'AS_USER open -a .*Keld Signal.app' "$c/calls" || fail "GUI install: Signal does not open when the install finishes"
grep -q 'install-sidecar' "$c/home/Library/LaunchAgents/"*.plist 2>/dev/null \
  && fail "GUI install: postinstall fetches the analysis engine; the daemon owns that (GET/POST /v1/engine)"
grep -q 'bootstrap' "$c/calls" && fail "GUI install: a fetch job was bootstrapped into launchd"
grep -qiE 'Terminal|onboard' "$c/calls" && fail "GUI install: a Terminal or onboarding script was opened"
# Registration must come before the app opens, or the app's first frame is "not running".
reg_n="$(grep -n 'keld-agent install' "$c/calls" | head -1 | cut -d: -f1)"
open_n="$(grep -n 'open -a' "$c/calls" | head -1 | cut -d: -f1)"
[ "$reg_n" -lt "$open_n" ] || fail "GUI install: Signal opens before the agent is registered"

# B. Command-line install: `sudo installer -pkg … -target /` (MDM, CI, a script).
c="$(run_case cli 1 0)"
grep -q 'AS_USER .*keld-agent install' "$c/calls" || fail "CLI install: the agent is not registered"
grep -q 'open' "$c/calls" && fail "CLI install: something was opened on a machine nobody may be watching"
grep -q 'bootstrap' "$c/calls" || fail "CLI install: the analysis engine fetch was not started"
grep -q 'install-sidecar' "$c/home/Library/LaunchAgents/co.keld.sidecar-fetch.plist" \
  || fail "CLI install: the fetch job does not run install-sidecar"

# C. No COMMAND_LINE_INSTALL but no Installer.app either: an MDM agent installing
#    through installd directly. Nobody clicked anything, so it is treated as
#    silent: the engine is fetched and nothing opens on the user's screen.
c="$(run_case mdm "" 0)"
grep -q 'open' "$c/calls" && fail "installd-driven install with no Installer.app: Signal opened on its own"
grep -q 'bootstrap' "$c/calls" || fail "installd-driven install: the engine fetch was not started"

echo "postinstall_test.sh: OK"
