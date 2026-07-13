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
echo
[ "$fails" = 0 ] && echo "build-freeze gate: all passed" || { echo "$fails failed"; exit 1; }
