#!/usr/bin/env bash
# onboard.command and onboard.cmd are GONE, and this file pins that.
#
# It used to pin onboard.command's contract (redeem a setup code in Terminal,
# fall back to a browser login, fetch the sidecar). Both scripts were deleted on
# 2026-09-29 — D10 of docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html
# — because no installer opens them any more: the pkg opens Keld Signal.app and
# the Windows installer opens the page, and Signal asks on first open. An unused
# script that prompts for a code confuses the next person who finds it, and a
# revived one would bring the installer prompt back by the side door.
#
# The name is kept because .github/workflows/ci.yml runs this file by path.
# MDM is unaffected: it has always paired with `keld-agent install --code <CODE>`
# (docs/install.md), never with either script.
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$d/../.." && pwd)"
fail() { echo "FAIL: $1"; exit 1; }

[ ! -e "$d/onboard.command" ] || fail "installers/macos/onboard.command is back; no installer prompts for a setup code"
[ ! -e "$root/installers/windows/onboard.cmd" ] || fail "installers/windows/onboard.cmd is back; no installer prompts for a setup code"

# Nothing that SHIPS or RUNS may name them: the pkg build, postinstall, the .iss,
# the curl installers and the release workflow. Comments are stripped, because
# the history of why they are gone is written in those comments.
strip() { sed -E 's/(^|[[:space:]])(#|;|\/\/).*$//' "$1"; }
for f in \
  "$d/build-pkg.sh" \
  "$d/scripts/postinstall" \
  "$root/installers/windows/keld-agent.iss" \
  "$root/scripts/install.sh" \
  "$root/scripts/install.ps1" \
  "$root/.github/workflows/installers.yml"; do
  [ -f "$f" ] || fail "cannot find $f - this guard would pass vacuously"
  if strip "$f" | grep -qE 'onboard\.(command|cmd)'; then
    fail "${f#"$root"/} still stages, opens or fetches an onboarding script"
  fi
done

echo "onboard_command_test.sh: OK (no onboarding script ships or runs)"
