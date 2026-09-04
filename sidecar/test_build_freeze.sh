#!/usr/bin/env bash
# Dev-runnable test of build-freeze.sh's obfuscation GATE (not the freeze itself).
# The full obfuscated freeze is exercised by `make obfuscate-check`.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
fails=0
check() { # desc expected_exit env...
  local desc="$1" want="$2"; shift 2
  env "$@" bash "$here/build-freeze.sh" --check >/dev/null 2>&1
  local got=$?
  if [ "$got" = "$want" ]; then echo "PASS $desc"; else echo "FAIL $desc (exit $got, want $want)"; fails=$((fails+1)); fi
}
# Obfuscation OFF -> gate passes regardless of tools.
check "flag off -> ok" 0 KELD_OBFUSCATE=0
# Obfuscation ON but tools forced absent -> hard-fail (never ship plain).
check "flag on, no tools -> hard-fail" 1 KELD_OBFUSCATE=1 PYARMOR_FORCE_MISSING=1

# The VERSION stamp must survive refactors of this script. A full freeze is far
# too heavy to run here (torch + PyInstaller, minutes), so this is a static
# assertion — and it reads the script with COMMENTS STRIPPED, because the
# onboard.command test learned the hard way that an assertion satisfiable by
# prose keeps passing after the real line goes away.
#
# What the stamp buys, and why losing it is silent: without it onboard.command
# re-downloads ~300MB on every run and the daemon cannot tell the two halves of
# an install apart. See scripts/check-sidecar-stamp.sh, which gates the built
# artifact, and the discovery doc for what that silence cost.
code_only="$(sed 's/#.*//' "$here/build-freeze.sh")"
if ! printf '%s' "$code_only" | grep -qF 'dist/keld-agent-sidecar/VERSION'; then
  echo "FAIL: build-freeze.sh no longer stamps dist/keld-agent-sidecar/VERSION"
  exit 1
fi
echo "PASS: build-freeze stamps the tree with its version"
if ! printf '%s' "$code_only" | grep -qF '${KELD_VERSION:-dev}'; then
  echo "FAIL: the stamp must fall back to dev — a local freeze has no tag, and both"
  echo "      readers treat dev as 'cannot tell' rather than as skew"
  exit 1
fi
echo "PASS: an untagged freeze stamps dev"
echo
[ "$fails" = 0 ] && echo "build-freeze gate: all passed" || { echo "$fails failed"; exit 1; }
