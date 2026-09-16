#!/usr/bin/env bash
# Static assertions over postinstall. Every one of these pins a failure that is
# silent on a real machine.
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
p="$d/scripts/postinstall"
code="$(sed 's/#.*//' "$p")"
fail() { echo "FAIL: $1"; exit 1; }

# ⚠️ -H or the LaunchAgent lands in /var/root/Library/LaunchAgents and never loads:
# service_darwin.go builds the plist path from os.UserHomeDir(), i.e. $HOME.
printf '%s' "$code" | grep -qF 'sudo -u "$user" -H' || fail "user-side commands must run with sudo -H"

# The agent must be registered by postinstall now — nothing else does it.
printf '%s' "$code" | grep -qF 'keld-agent install' || fail "postinstall never registers the agent"

# The handoff is what the wizard pane leaves behind.
printf '%s' "$code" | grep -qF 'installer-handoff.json' || fail "postinstall ignores the wizard handoff"

# ⚠️ THE FALLBACK IS MANDATORY. The plugin can fail to load with NO diagnostic
# (stale signature, bad plist). Without this, such a machine installs and then
# sits unconfigured with nothing on screen ever having asked for a code.
printf '%s' "$code" | grep -qF 'onboard.command' || fail "no fallback when the wizard pane did not run"

# The tool half must be pointed at the INSTALLED keld, never the plugin's copy.
printf '%s' "$code" | grep -qF -- '--bin-path' || fail "signal setup must pin the installed keld path"

# ⚠️ THE FALLBACK MUST NOT FIRE FOR "SET UP LATER". Gating on hook.json ALONE
# also fires for a person who deliberately deferred setup in the pane — opening
# a Terminal at someone who just chose "Set up later" is exactly what this
# branch exists to stop. The condition is two-part: the pane never ran (no
# handoff file existed, captured in had_handoff BEFORE the handoff is deleted)
# AND the machine ended up unconfigured (no hook.json).
printf '%s' "$code" | grep -qF 'had_handoff=false' || fail "postinstall must track whether the handoff existed before it was deleted"
printf '%s' "$code" | grep -qF '[ "$had_handoff" = false ] && [ ! -f "$userhome/.keld/hook.json" ]' || fail "fallback must require both: no handoff AND no hook.json"

# ── IMPORTANT 3: a handoff from a CANCELLED or otherwise DIFFERENT build is
# left behind because the pane writes it before the commit point, and this
# script only deletes it on a run that gets that far. A later install whose
# plugin fails to load silently — the one case the fallback above exists for —
# must not trust a stale file's "paired: false" and disable that fallback. The
# handoff's own `version` field is what discriminates "stale" from "current".
printf '%s' "$code" | grep -qF 'plutil -extract version raw -o - "$HANDOFF"' \
  || fail "postinstall never reads the handoff's own version"
printf '%s' "$code" | grep -qF '[ "$hv" != "$pv" ]' \
  || fail "postinstall does not compare the handoff version against \$PREFIX/VERSION before trusting it"

# ── IMPORTANT 1: an empty tool selection (every checkbox unticked in the pane)
# must configure NOTHING — distinct from "the pane never ran", which must
# still configure every detected adapter (today's unfiltered `signal setup`
# behaviour, tools.Select(nil)). had_handoff is the only signal that tells the
# two apart. Extract the real block (rather than re-typing its logic here) and
# run it under a stubbed `asuser` that records what it was asked to do, so a
# regression in the ACTUAL script fails this test, not just a copy of it.
tool_block="$(awk '/^if \[ "\$paired" = "true"/,/^fi$/' "$p")"
[ -n "$tool_block" ] || fail "could not extract the tool-configuration block for testing"

run_tool_case() { # $1=had_handoff $2=paired $3=tools -> prints the number of `asuser ... signal setup ...` calls
  ( set +e
    had_handoff="$1"; paired="$2"; tools="$3"; api_url=""; PREFIX="/usr/local/keld"
    calls="$(mktemp)"; trap 'rm -f "$calls"' EXIT
    asuser() { printf '%s\n' "$*" >> "$calls"; }
    eval "$tool_block"
    n="$(grep -c 'signal setup' "$calls" 2>/dev/null)"
    # grep -c exits 1 on a zero count even though it still prints "0"; the
    # caller runs under `set -euo pipefail`, so the raw exit status must never
    # escape this subshell — only the printed count may.
    echo "$n"
    true
  )
}

r="$(run_tool_case true true "")"
[ "$r" = "0" ] || fail "an empty tool selection from a REAL handoff (had_handoff=true) still ran signal setup — got $r call(s), expected 0"

r="$(run_tool_case false true "")"
[ "$r" = "1" ] || fail "the pane-never-ran case (had_handoff=false) must still configure every detected tool — got $r call(s), expected 1"

r="$(run_tool_case true true "claude_code")"
[ "$r" = "1" ] || fail "a real, non-empty tool selection did not configure anything — got $r call(s), expected 1"

# ⚠️ THE FALLBACK FETCH MUST OUTLIVE THIS SCRIPT, AND MUST LEAVE A RECORD.
# It used to be a bare `sh -c "keld signal install-sidecar … >/dev/null 2>&1 &"`.
# Measured on a real v3.0.2 install (2026-09-16): postinstall ran at 10:40:34,
# this path fired, and two minutes later there was no fetch process, no staging
# dir, and a sidecar tree still carrying its previous mtime — while the same
# command run by hand worked first time. A bare background child does not
# survive PackageKit tearing down the script's sandbox, and /dev/null meant
# nothing said so.
#
# Anyone ALREADY SIGNED IN takes this path: they reach Continue before the
# pane's ~190MB download finishes, so no tree is staged. The faster the person,
# the likelier the sidecar silently never updates.
grep -q 'launchctl bootstrap' "$p" \
  || fail "the fallback sidecar fetch is not handed to launchd, so it dies with postinstall"
grep -q 'sidecar-install.log' "$p" \
  || fail "the fallback sidecar fetch writes no log, so a silent failure stays silent"
if grep -E 'install-sidecar[^|]*>/dev/null 2>&1 *&' "$p" >/dev/null; then
  fail "the fallback still backgrounds the fetch into /dev/null — the exact shape that achieved nothing"
fi

echo "postinstall_test.sh: OK"
