#!/usr/bin/env bash
# Functional test of the launchd job postinstall writes for the fallback sidecar
# fetch. It RUNS the generation block with stand-in paths and asserts on the
# plist that comes out, rather than grepping the generator — every bug this pins
# produced a generator that read correctly and emitted something else.
#
# ⚠️ WHY THIS EXISTS. The fallback path (no staged tree — i.e. anyone already
# signed in, who reaches Continue before the ~190MB download finishes) used to
# be a bare background child redirected to /dev/null. It did not survive
# PackageKit tearing down the script's sandbox: measured on a real v3.0.2
# install (2026-09-16) — no fetch process, no staging dir, a sidecar tree with
# its previous mtime, and nothing said. Handing the work to launchd fixed that,
# and then three further defects surfaced on local installs, each of which this
# test now catches:
#
#   1. printf REUSES ITS FORMAT until its arguments run out, so a line written
#      with two arguments against one conversion was emitted twice, the first
#      redirecting into a file named after the release tag.
#   2. The job removed its plist AFTER `launchctl bootout` of its own label,
#      which terminates the script before it gets there: exit=0 was logged and
#      both files remained, so the fetch would re-run at every login.
#   3. ⚠️ macOS NAMES THE BACKGROUND ITEM AFTER THE PROGRAM AND SHOWS IT TO THE
#      PERSON INSTALLING. Running a generated shell script produced
#      "'.sidecar-fetch.sh' can run in the background" — internal jargon, in a
#      system notification, to someone who just wanted to install Keld. The job
#      must therefore run the SIGNED BINARY, with launchd doing the logging.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
src="$here/scripts/postinstall"
fail() { echo "FAIL: $1"; exit 1; }

[ -f "$src" ] || fail "postinstall not found at $src"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

PREFIX=/usr/local/keld
userhome="$work/home"
mkdir -p "$userhome/.keld/logs" "$userhome/Library/LaunchAgents"
tag="v9.9.9"
fetch_log="$userhome/.keld/logs/sidecar-install.log"
plist="$userhome/Library/LaunchAgents/co.keld.sidecar-fetch.plist"

# Run postinstall's plist-generation block verbatim.
awk '/^  args="<string>/,/^PLIST$/' "$src" > "$work/block.sh"
[ -s "$work/block.sh" ] || fail "could not extract the plist-generation block from postinstall"
# shellcheck disable=SC1090
. "$work/block.sh"

[ -s "$plist" ] || fail "the generation block produced no plist"
got="$(cat "$plist")"

# 1. It runs the signed binary, and NOTHING that looks like a script. This is
#    the assertion that keeps a system notification from showing internal names.
printf '%s' "$got" | grep -q "<string>$PREFIX/keld</string>" \
  || fail "the job does not run the installed keld binary, so macOS will name whatever it does run"
if printf '%s' "$got" | grep -qE '<string>[^<]*\.(sh|command|py)</string>'; then
  fail "the job runs a script; macOS shows that file's name to the person installing"
fi
if printf '%s' "$got" | grep -q 'sidecar-fetch.sh'; then
  fail "the generated shell runner is back — see the notification this test exists for"
fi

# 2. The tag is interpolated, not left as a conversion specifier, and appears once.
printf '%s' "$got" | grep -q "<string>$tag</string>" \
  || fail "the release tag is not in the job's arguments"
if printf '%s' "$got" | grep -q '%s'; then
  fail "an unconsumed printf conversion reached the generated plist"
fi
n=$(printf '%s\n' "$got" | grep -c 'install-sidecar' || true)
[ "$n" -eq 1 ] || fail "expected 1 install-sidecar invocation, got $n"

# 3. The job deletes itself after success — without this it re-downloads ~190MB
#    at every login — and does so through the binary, not a bootout of its own
#    label, which would kill the process doing the cleaning.
printf '%s' "$got" | grep -q -- '--cleanup-job' \
  || fail "the job never removes its own plist, so the fetch re-runs at every login"
printf '%s' "$got" | grep -q "<string>$plist</string>" \
  || fail "--cleanup-job is not given the plist's own path"
if printf '%s' "$got" | grep -q 'launchctl bootout'; then
  fail "the job boots out its own label, terminating itself before it can clean up"
fi

# 4. launchd does the logging, so a silent failure cannot happen again.
printf '%s' "$got" | grep -q "<key>StandardOutPath</key><string>$fetch_log</string>" \
  || fail "the job writes no log, so a silent failure stays silent"
printf '%s' "$got" | grep -q "<key>StandardErrorPath</key><string>$fetch_log</string>" \
  || fail "the job discards stderr, which is where a failed fetch explains itself"

echo "postinstall fetch-job: all checks passed"
