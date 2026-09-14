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

echo "postinstall_test.sh: OK"
